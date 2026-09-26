// Package sse reads the data payloads of a server-sent event stream, the wire format
// every Kubemoot discussion stream uses. It is the one reader shared by the crew
// liaison, the fitness runner, and the operator's deferred judge, so the three agree
// on what a data line is.
package sse

import (
	"bufio"
	"io"
	"strings"
)

// Line sizes: a synthesis can be long; a line beyond maxLine is an error, not a hang.
const (
	initialBuffer = 64 * 1024
	maxLine       = 4 * 1024 * 1024
)

// Data calls fn with the payload of every "data:" line, with the prefix and
// surrounding whitespace removed. Blank payloads and the "[DONE]" sentinel are
// skipped; other line kinds (comments, "event:", "id:") are ignored. Reading stops
// when fn returns false or the stream ends. The error is the reader's, if any.
func Data(r io.Reader, fn func(data string) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialBuffer), maxLine)
	for scanner.Scan() {
		data, ok := payload(scanner.Text())
		if !ok {
			continue
		}
		if !fn(data) {
			return nil
		}
	}
	return scanner.Err()
}

// payload extracts a data line's payload; ok is false for any other line.
func payload(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || rest == "[DONE]" {
		return "", false
	}
	return rest, true
}
