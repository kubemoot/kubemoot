package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type headerFixture struct {
	RoundTrip  []struct{ Name, Value, Encoded string } `json:"roundTrip"`
	DecodeOnly []struct{ Name, Encoded, Value string } `json:"decodeOnly"`
}

func loadHeaderFixture(t *testing.T) headerFixture {
	t.Helper()
	raw, err := os.ReadFile("../../../test/fixtures/header-values.json")
	if err != nil {
		t.Fatal(err)
	}
	var f headerFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.RoundTrip) == 0 || len(f.DecodeOnly) == 0 {
		t.Fatal("the fixture has no cases")
	}
	return f
}

func TestHeaderValueSharedFixture(t *testing.T) {
	f := loadHeaderFixture(t)
	for _, c := range f.RoundTrip {
		t.Run(c.Name, func(t *testing.T) {
			if got := EncodeHeaderValue(c.Value); got != c.Encoded {
				t.Errorf("EncodeHeaderValue(%q) = %q, want %q", c.Value, got, c.Encoded)
			}
			if got := DecodeHeaderValue(c.Encoded); got != c.Value {
				t.Errorf("DecodeHeaderValue(%q) = %q, want %q", c.Encoded, got, c.Value)
			}
		})
	}
	for _, c := range f.DecodeOnly {
		t.Run(c.Name, func(t *testing.T) {
			if got := DecodeHeaderValue(c.Encoded); got != c.Value {
				t.Errorf("DecodeHeaderValue(%q) = %q, want %q", c.Encoded, got, c.Value)
			}
		})
	}
}

// EncodeHeaderValue mirrors the runtime's encoder (HeaderValue in agent-runtime), the only writer,
// so the shared fixture can pin both directions.
func EncodeHeaderValue(v string) string {
	if !needsHeaderEncoding(v) {
		return v
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.WriteString(HeaderValueMarker)
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 0x20 && c < 0x7F && c != '%' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xF])
	}
	return b.String()
}

func needsHeaderEncoding(v string) bool {
	if strings.HasPrefix(v, HeaderValueMarker) {
		return true
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] >= 0x7F {
			return true
		}
	}
	return false
}
