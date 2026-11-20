/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package logcontent

import (
	"bytes"
	"testing"

	"github.com/go-logr/logr/funcr"
)

func TestEnabledIsOffUnlessTrue(t *testing.T) {
	cases := map[string]bool{"": false, "false": false, "no": false, "garbage": false, "true": true, " TRUE ": true, "1": false, "t": false}
	for value, want := range cases {
		t.Setenv(Env, value)
		if got := Enabled(); got != want {
			t.Errorf("%s=%q: Enabled() = %v, want %v", Env, value, got, want)
		}
	}
}

func TestSizeNamesTheLengthOnly(t *testing.T) {
	if got := Size("secret text"); got != "11 chars" {
		t.Errorf("Size = %q", got)
	}
	if got := Size("caf\u00e9"); got != "4 chars" {
		t.Errorf("Size counts characters, not bytes: %q", got)
	}
}

func TestDebugWritesOnlyWhenOn(t *testing.T) {
	var buf bytes.Buffer
	log := funcr.New(func(prefix, args string) { buf.WriteString(args) }, funcr.Options{Verbosity: 1})

	t.Setenv(Env, "false")
	Debug(log, "a line", "text", "the question")
	if buf.Len() != 0 {
		t.Fatalf("switch off: wrote %q", buf.String())
	}

	t.Setenv(Env, "true")
	Debug(log, "a line", "text", "the question")
	if !bytes.Contains(buf.Bytes(), []byte("the question")) {
		t.Fatalf("switch on: line missing, got %q", buf.String())
	}
}
