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

package scheduler

import "testing"

func TestDiscussMessageCarriesEveryField(t *testing.T) {
	meta := map[string]any{"k": 1}
	msg := discussMessage("t-1", schedulerAgentName, "reminder", "water the plants", "ops", "2026-10-04T00:00:00Z", meta)

	want := map[string]any{
		"threadId":    "t-1",
		"agentName":   schedulerAgentName,
		"messageType": "reminder",
		"content":     "water the plants",
		"channel":     "ops",
		"timestamp":   "2026-10-04T00:00:00Z",
	}
	for k, v := range want {
		if msg[k] != v {
			t.Errorf("%s = %v, want %v", k, msg[k], v)
		}
	}
	if m, ok := msg["metadata"].(map[string]any); !ok || m["k"] != 1 {
		t.Errorf("metadata = %v", msg["metadata"])
	}
	if id, _ := msg["messageId"].(string); id == "" {
		t.Error("messageId must be set")
	}
	if other := discussMessage("t-1", schedulerAgentName, "reminder", "", "ops", "", nil); other["messageId"] == msg["messageId"] {
		t.Error("each message needs its own messageId")
	}
}
