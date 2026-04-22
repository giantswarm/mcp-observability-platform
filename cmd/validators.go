package cmd

import (
	"fmt"
	"math"
	"strings"
)

// splitAndTrimCSV splits a comma-separated env var value, trims whitespace
// around each entry, and drops empty results. "" → nil (not []string{""}).
func splitAndTrimCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateEncryptionKeyEntropy rejects obviously weak `MCP_OAUTH_ENCRYPTION_KEY`
// values (all-zero, repeated byte, low distinct-byte count). Catches
// copy-paste accidents like `0000…` or `aaaa…` before anything is
// encrypted in production.
//
// Threshold: 4.0 bits/byte of Shannon entropy over a 32-byte key. A
// uniformly random 32-byte key averages ~4.9 bits/byte (upper bound is
// log2(32)=5.0 because entropy is capped by distinct-symbol count, not
// the full 8 bits/byte of the alphabet). 4.0 rejects catastrophic inputs
// while leaving room for legitimate keys with modest non-uniformity.
func validateEncryptionKeyEntropy(key []byte) error {
	if len(key) == 0 {
		return nil
	}
	if len(key) < 32 {
		return fmt.Errorf("MCP_OAUTH_ENCRYPTION_KEY is too short: %d bytes (want at least 32)", len(key))
	}
	const minBitsPerByte = 4.0
	entropy := shannonEntropy(key)
	if entropy < minBitsPerByte {
		return fmt.Errorf("MCP_OAUTH_ENCRYPTION_KEY has low entropy (%.2f bits/byte, want >= %.1f) — check that the key is not all zeros, a repeated character, or a placeholder", entropy, minBitsPerByte)
	}
	return nil
}

// shannonEntropy returns the Shannon entropy of b in bits per byte.
// Uniformly random bytes approach log2(len(b)) capped at 8.0; a sequence
// of a single repeated byte is 0.0.
func shannonEntropy(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var counts [256]int
	for _, c := range b {
		counts[c]++
	}
	total := float64(len(b))
	var entropy float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}
