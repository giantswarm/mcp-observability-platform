package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestSplitAndTrimCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,,", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
		{"a, ,b", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitAndTrimCSV(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitAndTrimCSV(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitAndTrimCSV(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestValidateEncryptionKeyEntropy(t *testing.T) {
	cases := []struct {
		name    string
		key     []byte
		wantErr bool
	}{
		{"empty (no key configured)", nil, false},
		{"32 zero bytes — placeholder", bytes.Repeat([]byte{0}, 32), true},
		{"32 repeated 0xff — placeholder", bytes.Repeat([]byte{0xff}, 32), true},
		{"32 repeated 'a' — typo", bytes.Repeat([]byte{'a'}, 32), true},
		{"16 bytes — too short", bytes.Repeat([]byte{0xaa, 0x55}, 8), true},
		{"random-looking 32 bytes", []byte{
			0x3f, 0xa9, 0x17, 0x4d, 0xe2, 0x0c, 0x5b, 0x71,
			0x8e, 0x24, 0xc7, 0x93, 0x06, 0xf8, 0xab, 0x62,
			0x19, 0x5d, 0x7e, 0xa0, 0x4b, 0xcf, 0x38, 0x82,
			0x1c, 0x65, 0xd4, 0x0f, 0x97, 0x2b, 0xe6, 0x51,
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateEncryptionKeyEntropy(c.key)
			if c.wantErr && err == nil {
				t.Errorf("validateEncryptionKeyEntropy: want error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("validateEncryptionKeyEntropy: want nil, got %v", err)
			}
			if c.wantErr && err != nil {
				// Check the message points at the right cause to help operators.
				if !strings.Contains(err.Error(), "OAUTH_ENCRYPTION_KEY") {
					t.Errorf("error should mention the env var name: %v", err)
				}
			}
		})
	}
}

func TestShannonEntropy(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want float64 // expected bits/byte within 0.01
	}{
		{"empty", nil, 0},
		{"single byte", []byte{0x42}, 0},
		{"all same byte", bytes.Repeat([]byte{0xab}, 100), 0},
		// Two distinct bytes, perfectly balanced → log2(2) = 1.0 bit/byte.
		{"two values balanced", append(bytes.Repeat([]byte{0}, 50), bytes.Repeat([]byte{1}, 50)...), 1.0},
		// 256 distinct values, each once → log2(256) = 8.0 bits/byte.
		{"full-alphabet once each", fullAlphabet(), 8.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shannonEntropy(c.in)
			if got < c.want-0.01 || got > c.want+0.01 {
				t.Errorf("shannonEntropy = %.4f, want %.4f (±0.01)", got, c.want)
			}
		})
	}
}

func fullAlphabet() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
