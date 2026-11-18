package workspace

import (
	"strings"
	"testing"
)

func TestNeutralizeTurnsControlCharactersIntoSpaces(t *testing.T) {
	in := "a\x1b[31mb c d\x7fe\u0085f\tg"
	got := neutralize(in)
	if strings.ContainsAny(got, "\x1b  \x7f\u0085\t") {
		t.Errorf("neutralize(%q) = %q still holds a control character", in, got)
	}
	if want := "a [31mb c d e f g"; got != want {
		t.Errorf("neutralize = %q, want %q", got, want)
	}
	if neutralize("café \U0001F680") != "café \U0001F680" {
		t.Error("printable text changed")
	}
}

func TestCatalogLineAndArgsCellNeutralizeControlCharacters(t *testing.T) {
	e := catalogEntry{key: "k", tool: "t\x1b[2Jool", args: "{\"a\":\"x\x1by z\"}", typ: mediaPlain, rows: -1}
	if line := e.line(); strings.ContainsAny(line, "\x1b ") {
		t.Errorf("catalog line carries a control character: %q", line)
	}
	if got := cellFor(argsColumn, prepared{args: "x\x1by"}, nil, nil); got != "x y" {
		t.Errorf("_args cell = %q", got)
	}
	if got := cellFor("_arg_a", prepared{}, map[string]string{"a": "p q"}, map[string]string{"_arg_a": "a"}); got != "p q" {
		t.Errorf("_arg_a cell = %q", got)
	}
}
