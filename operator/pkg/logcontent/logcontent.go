/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package logcontent is the one switch for content in logs. Kubemoot logs carry
// ids, sizes, timings and outcomes. They do not carry what people asked, what a
// crew answered, prompt text, tool arguments or tool results. An installer who
// needs that text to debug sets KUBEMOOT_LOG_CONTENT=true; Debug then writes the
// lines that carry it, at verbosity 1. With the switch off those lines are not
// written at all.
//
// The Java services read the same variable (their LogContent class).
package logcontent

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/go-logr/logr"
)

// Env is the environment variable that turns content in logs on.
const Env = "KUBEMOOT_LOG_CONTENT"

// Enabled reports whether the installer turned content in logs on. Off unless
// the variable is "true" (any case).
func Enabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(Env)), "true")
}

// Size says how big a text is, for a line that must not carry the text.
func Size(text string) string {
	return fmt.Sprintf("%d chars", utf8.RuneCountInString(text))
}

// Debug writes a line that carries content, at verbosity 1, and only when the
// switch is on.
func Debug(log logr.Logger, msg string, keysAndValues ...any) {
	if Enabled() {
		log.V(1).Info(msg, keysAndValues...)
	}
}
