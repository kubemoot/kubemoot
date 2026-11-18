package threadkey

import (
	"strings"
	"testing"
)

const (
	thread = "crew-homelab-pilot/homelab-pilot/0071d018-24cb-4532-a560-fc12a788e654/"
	short  = "k8s-workloads/002-resources_list"

	errNotValid = "not valid"
	errOther    = "another thread"
)

func scoped() Scope { return FromMeta(map[string]any{MetaKey: thread}) }

func TestResolve(t *testing.T) {
	other := "crew-homelab-pilot/homelab-pilot/0071d018-24cb-45-32-a560-fc12a788e654/k8s-workloads/002-resources_list"
	tests := []struct {
		name    string
		scope   Scope
		key     string
		want    string
		wantErr string
	}{
		{"short key resolves inside the thread", scoped(), short, thread + short, ""},
		{"long key of the thread passes", scoped(), thread + short, thread + short, ""},
		{"pattern with a short key", scoped(), "k8s-workloads/*-resources_list", thread + "k8s-workloads/*-resources_list", ""},
		{"all calls of an agent", scoped(), "k8s-workloads/*", thread + "k8s-workloads/*", ""},
		{"mistyped thread id is refused", scoped(), other, "", errOther},
		{"key of another crew is refused", scoped(), "ns/other-crew/abc/agent/001-x", "", errOther},
		{"parent climb is refused", scoped(), "../other/001-x", "", errNotValid},
		{"dot part is refused", scoped(), "./001-x", "", errNotValid},
		{"empty part is refused", scoped(), "agent//001-x", "", errNotValid},
		{"climb out of the thread after its prefix", scoped(), thread + "../other/agent/001-x", "", errNotValid},
		{"empty part after the prefix", scoped(), thread + "agent//001-x", "", errNotValid},
		{"dot part after the prefix", scoped(), thread + "./agent/001-x", "", errNotValid},
		{"one-part key passes unchanged", scoped(), "001-x", "001-x", ""},
		{"empty key passes unchanged", scoped(), "", "", ""},
		{"no thread leaves a short key alone", Scope{}, short, short, ""},
		{"no thread leaves a long key alone", Scope{}, other, other, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.scope.Resolve(tc.key)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromMetaIgnoresBadValues(t *testing.T) {
	bad := []any{
		"", "ns/crew/thread", "ns/crew/", "ns/crew/thread/extra/", "ns/../thread/", "ns/crew/*/", "/crew/thread/",
		"ns\\crew/thread/", "ns/crew/thr ead/", "ns/crew/" + strings.Repeat("x", 600) + "/", 42, nil, []string{"ns/crew/thread/"},
	}
	for _, v := range bad {
		if p := FromMeta(map[string]any{MetaKey: v}).Prefix(); p != "" {
			t.Errorf("value %v gave prefix %q, want none", v, p)
		}
	}
	if p := FromMeta(nil).Prefix(); p != "" {
		t.Errorf("nil meta gave prefix %q", p)
	}
	if p := scoped().Prefix(); p != thread {
		t.Errorf("prefix %q, want %q", p, thread)
	}
}

func TestResolveAll(t *testing.T) {
	got, err := scoped().ResolveAll(map[string]string{"jobs": short, "all": "k8s-workloads/*-resources_list"})
	if err != nil {
		t.Fatal(err)
	}
	if got["jobs"] != thread+short || got["all"] != thread+"k8s-workloads/*-resources_list" {
		t.Fatalf("got %v", got)
	}
	_, err = scoped().ResolveAll(map[string]string{"x": "ns/crew/other/agent/001-y"})
	if err == nil || !strings.Contains(err.Error(), `artifacts["x"]`) {
		t.Fatalf("want an error naming the table, got %v", err)
	}
}
