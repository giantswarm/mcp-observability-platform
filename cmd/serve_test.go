package cmd

import (
	"strings"
	"testing"
)

func TestValidateTransport(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"streamable-http", true},
		{"stdio", false},
		{"sse", false},
		{"", false},
		{"HTTP", false},
	}
	for _, c := range cases {
		err := validateTransport(c.in)
		if c.wantOK && err != nil {
			t.Errorf("validateTransport(%q) = %v, want nil", c.in, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("validateTransport(%q) = nil, want error", c.in)
		}
		if !c.wantOK && err != nil && !strings.Contains(err.Error(), "not yet supported") {
			t.Errorf("validateTransport(%q) error = %v, want 'not yet supported'", c.in, err)
		}
	}
}

func TestDecodeEncryptionKey(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantLen int
		wantErr bool
	}{
		{"raw 32 bytes", strings.Repeat("x", 32), 32, false},
		{"hex 64 chars", strings.Repeat("ab", 32), 32, false},
		{"too short", "short", 0, true},
		{"empty", "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := decodeEncryptionKey(c.in)
			if c.wantErr && err == nil {
				t.Errorf("decodeEncryptionKey(%q) = nil, want error", c.in)
			}
			if !c.wantErr && err != nil {
				t.Errorf("decodeEncryptionKey(%q) = %v, want nil", c.in, err)
			}
			if !c.wantErr && len(b) != c.wantLen {
				t.Errorf("decodeEncryptionKey(%q) len = %d, want %d", c.in, len(b), c.wantLen)
			}
		})
	}
}
