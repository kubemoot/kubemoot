package mcpserver

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/workspace-access/internal/store"
)

const (
	otherThread  = "crew-homelab-pilot/homelab-pilot/11111111-2222-3333-4444-555555555555/"
	listedPods   = scopeThread + "k8s-workloads/001-pods_list"
	listedEvents = scopeThread + "k8s-workloads/002-events_list"
)

// listingFetcher serves objects that can be listed, as the NATS store does.
type listingFetcher struct {
	fakeFetcher
	headers map[string]map[string]string
}

func (l listingFetcher) List(_ context.Context, prefix string) ([]store.Meta, error) {
	var out []store.Meta
	for k, h := range l.headers {
		if strings.HasPrefix(k, prefix) {
			out = append(out, store.Meta{Key: k, Size: uint64(len(l.data[k])), Headers: h})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func listingServer() *Server {
	call := func(tool string, extra ...string) map[string]string {
		return callHeaders(append([]string{"gen_ai.tool.name", tool}, extra...)...)
	}
	return New(listingFetcher{
		fakeFetcher: fakeFetcher{data: map[string]string{listedPods: "[]", listedEvents: "x", otherThread + "k8s-workloads/001-pods_list": "[]"}},
		headers: map[string]map[string]string{
			listedPods:   call("pods_list", "Content-Type", "application/json", "kubemoot.rows", "0"),
			listedEvents: call("events_list", "kubemoot.error", "true"),
			otherThread + "k8s-workloads/001-pods_list": call("pods_list"),
		},
	})
}

func TestWorkspaceListIsOfferedOnlyWhenTheStoreCanList(t *testing.T) {
	if names := toolNames(t, newServer(nil)); contains(names, "workspace_list") {
		t.Fatalf("offered without a listing store: %v", names)
	}
	if names := toolNames(t, listingServer()); !contains(names, "workspace_list") {
		t.Fatalf("missing: %v", names)
	}
}

func TestWorkspaceListShowsOnlyTheCallersThread(t *testing.T) {
	got, err := callScoped(t, listingServer(), threadMeta(), "workspace_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "[CATALOG calls=2 errors=1 ") ||
		!strings.Contains(got, "key=k8s-workloads/001-pods_list tool=pods_list") ||
		!strings.Contains(got, "key=k8s-workloads/002-events_list tool=events_list") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "11111111") {
		t.Fatalf("listed another thread: %q", got)
	}
}

func TestWorkspaceListFiltersByTool(t *testing.T) {
	got, err := callScoped(t, listingServer(), threadMeta(), "workspace_list", map[string]any{"tool": "events_list"})
	if err != nil || !strings.HasPrefix(got, "[CATALOG calls=1 ") || strings.Contains(got, "pods_list") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWorkspaceListWithoutAThreadIsRefused(t *testing.T) {
	_, err := callScoped(t, listingServer(), nil, "workspace_list", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "names none") {
		t.Fatalf("got %v", err)
	}
	bad := map[string]any{"workspacePrefix": "not-a-prefix"}
	if _, err := callScoped(t, listingServer(), bad, "workspace_list", map[string]any{}); err == nil {
		t.Fatal("a malformed thread value must behave like none")
	}
}

// describingQuerier is a Querier that can also describe.
type describingQuerier struct {
	fakeQuerier
	described map[string]string
}

func (d *describingQuerier) Describe(_ context.Context, artifacts map[string]string) (string, error) {
	d.described = artifacts
	return "[TABLE p rows=0 columns=0]\n", nil
}

func TestWorkspaceDescribeIsOfferedOnlyWhenTheEngineCanDescribe(t *testing.T) {
	s := newServer(nil)
	s.Query = &fakeQuerier{}
	if names := toolNames(t, s); contains(names, "workspace_describe") {
		t.Fatalf("offered for an engine that cannot describe: %v", names)
	}
	s.Query = &describingQuerier{}
	if names := toolNames(t, s); !contains(names, "workspace_describe") {
		t.Fatalf("missing: %v", names)
	}
}

func TestWorkspaceDescribeResolvesKeysInsideTheThread(t *testing.T) {
	q := &describingQuerier{}
	s := newServer(nil)
	s.Query = q
	args := map[string]any{artifactsArg: map[string]any{"p": scopeShort, "every": "k8s-workloads/*-pods_list"}}

	got, err := callScoped(t, s, threadMeta(), "workspace_describe", args)

	if err != nil || got != "[TABLE p rows=0 columns=0]\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if q.described["p"] != scopeThread+scopeShort || q.described["every"] != scopeThread+"k8s-workloads/*-pods_list" {
		t.Fatalf("keys: %v", q.described)
	}
}

func TestWorkspaceDescribeRefusesAKeyOfAnotherThread(t *testing.T) {
	q := &describingQuerier{}
	s := newServer(nil)
	s.Query = q
	args := map[string]any{artifactsArg: map[string]any{"x": otherThread + scopeShort}}

	_, err := callScoped(t, s, threadMeta(), "workspace_describe", args)

	if err == nil || !strings.Contains(err.Error(), "another thread") || q.described != nil {
		t.Fatalf("want a refusal before describing, got %v (described %v)", err, q.described)
	}
}

func TestWorkspaceDescribeAcceptsTheMapAsAString(t *testing.T) {
	q := &describingQuerier{}
	s := newServer(nil)
	s.Query = q

	_, err := callScoped(t, s, threadMeta(), "workspace_describe",
		map[string]any{artifactsArg: `{"p": "` + scopeShort + `"}`})

	if err != nil || q.described["p"] != scopeThread+scopeShort {
		t.Fatalf("%v %v", err, q.described)
	}
}

func TestWorkspaceDescribeWithoutAThreadIsRefused(t *testing.T) {
	q := &describingQuerier{}
	s := newServer(nil)
	s.Query = q
	args := map[string]any{artifactsArg: map[string]any{"p": scopeThread + scopeShort}}

	for name, meta := range map[string]map[string]any{"no meta": nil, "a malformed prefix": {"workspacePrefix": "x"}} {
		if _, err := callScoped(t, s, meta, "workspace_describe", args); err == nil || !strings.Contains(err.Error(), "names none") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if q.described != nil {
		t.Fatal("nothing may be described outside a thread")
	}
}

func TestWorkspaceDescribeRefusesKeysOutsideTheThread(t *testing.T) {
	for _, key := range []string{"*", "x", "..", "a/*/b/c/d", "/etc/passwd"} {
		q := &describingQuerier{}
		s := newServer(nil)
		s.Query = q

		_, err := callScoped(t, s, threadMeta(), "workspace_describe", map[string]any{artifactsArg: map[string]any{"p": key}})

		if err == nil || q.described != nil {
			t.Errorf("key %q: want a refusal before describing, got %v (described %v)", key, err, q.described)
		}
	}
}
