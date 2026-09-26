package artifact

import (
	"path/filepath"
	"testing"
)

func TestParseMessage(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantKey string
		wantErr bool
	}{
		{
			name:    "valid reference",
			content: `{"artifact":{"bucket":"kubemoot_discussion_artifacts","key":"pilot/conv1/thread1/k8s-config/resources_list-0","rows":247}}`,
			wantKey: "pilot/conv1/thread1/k8s-config/resources_list-0",
		},
		{name: "ordinary inline content", content: "default: 3 ConfigMaps", wantErr: true},
		{name: "empty artifact object", content: `{"artifact":{}}`, wantErr: true},
		{name: "artifact without key", content: `{"artifact":{"bucket":"b"}}`, wantErr: true},
		{name: "not json", content: "{ broken", wantErr: true},
		{name: "json but no artifact field", content: `{"other":1}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseMessage(tc.content)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ref %+v", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.Key != tc.wantKey {
				t.Fatalf("key = %q, want %q", ref.Key, tc.wantKey)
			}
		})
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	in := Reference{Bucket: "b", Key: "c/x/y/agent/tool-0", Rows: 5, Preview: "head"}
	s, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := ParseMessage(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Key != in.Key || out.Rows != in.Rows || out.Preview != in.Preview {
		t.Fatalf("round trip mismatch: %+v vs %+v", out, in)
	}
}

func TestLocalPath(t *testing.T) {
	base := filepath.FromSlash("/artifacts")
	good, err := LocalPath(base, "pilot/conv1/thread1/k8s-config/resources_list-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(base, "pilot", "conv1", "thread1", "k8s-config", "resources_list-0")
	if good != want {
		t.Fatalf("path = %q, want %q", good, want)
	}

	for _, bad := range []string{
		"",
		"../escape",
		"pilot/../../etc/passwd",
		"a/b/../../../../../../etc/shadow",
	} {
		if _, err := LocalPath(base, bad); err != ErrUnsafeKey {
			t.Fatalf("key %q: expected ErrUnsafeKey, got %v", bad, err)
		}
	}
}
