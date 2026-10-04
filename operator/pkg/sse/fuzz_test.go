/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sse

import (
	"strings"
	"testing"
)

// FuzzData feeds an arbitrary event stream (the discussion gateway's response
// body). Every payload handed to the callback is trimmed, non-empty, not the
// [DONE] sentinel, and present in the stream; stopping after the first payload
// stops the read.
func FuzzData(f *testing.F) {
	f.Add(": a comment\nevent: phase\ndata: {\"type\":\"a\"}\ndata:{\"type\":\"b\"}\n" +
		"  data:   {\"type\":\"c\"}  \ndata:\ndata: [DONE]\n\nid: 7\ndata: {\"type\":\"d\"}\n")
	f.Add("data: {\"type\":\"synthesis\",\"content\":\"done\"}\r\n\r\n")
	f.Add("data")
	f.Fuzz(func(t *testing.T, stream string) {
		calls := 0
		_ = Data(strings.NewReader(stream), func(d string) bool {
			calls++
			if d == "" || d == "[DONE]" || d != strings.TrimSpace(d) || !strings.Contains(stream, d) {
				t.Fatalf("bad payload %q from stream %q", d, stream)
			}
			return false
		})
		if calls > 1 {
			t.Fatalf("callback ran %d times after asking to stop", calls)
		}
	})
}
