package store

import (
	"strings"
	"unicode/utf8"
)

// HeaderValueMarker starts every encoded header value. The agent runtime (HeaderValue in
// agent-runtime) writes a header value that is not printable ASCII, or that starts with the marker,
// as the marker followed by the text's UTF-8 bytes with each byte outside printable ASCII (and the
// percent sign) as %XX. A value without the marker is the text itself, so objects stored before the
// encoding existed read as before; a legacy plain value that starts with the marker and has only
// valid escapes after it reads as encoded, as in Java. Both sides run test/fixtures/header-values.json.
const HeaderValueMarker = "utf8-pct:"

// DecodeHeaderValue returns the text a header value stands for. A value that is not validly encoded
// is returned as it is.
func DecodeHeaderValue(v string) string {
	rest, ok := strings.CutPrefix(v, HeaderValueMarker)
	if !ok {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c < 0x20 || c >= 0x7F {
			return v
		}
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(rest) {
			return v
		}
		hi, lo := hexDigit(rest[i+1]), hexDigit(rest[i+2])
		if hi < 0 || lo < 0 {
			return v
		}
		b.WriteByte(byte(hi<<4 | lo))
		i += 2
	}
	if !utf8.ValidString(b.String()) {
		return v
	}
	return b.String()
}

// hexDigit is the value of an ASCII hex digit, or -1.
func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
