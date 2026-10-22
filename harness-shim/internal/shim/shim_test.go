package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	binSh       = "/bin/sh"
	modeNone    = "none"
	modeAll     = "all"
	keyDeadline = "deadlineSeconds"
	promptTag   = "{{promptFile}}"
	gooseCmd    = "/usr/local/bin/goose"
	subRun      = "run"
)

func body(question string, profile map[string]any) []byte {
	parts := []any{map[string]any{"text": question}}
	if profile != nil {
		parts = append(parts, map[string]any{"data": profile})
	}
	b, _ := json.Marshal(map[string]any{"message": map[string]any{"contextId": "thread-1", "role": "ROLE_USER", "parts": parts}})
	return b
}

func newTestServer(t *testing.T, cfg Config, env Environment) (*Server, *httptest.Server) {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, NewRunner(cfg, env), "http://127.0.0.1:9000")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

type reply struct {
	Task struct {
		ID     string `json:"id"`
		Status struct {
			State   string `json:"state"`
			Message struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"status"`
		Artifacts []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"artifacts"`
	} `json:"task"`
}

func (r reply) text() string { return r.Task.Status.Message.Parts[0].Text }

func post(t *testing.T, url string, b []byte) (int, reply) {
	t.Helper()
	resp, err := http.Post(url, mediaType, bytes.NewReader(b)) //nolint:gosec,noctx // test server
	if err != nil {
		t.Error(err)
		return 0, reply{}
	}
	defer func() { _ = resp.Body.Close() }()
	var r reply
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return resp.StatusCode, r
}

func sh(script string) []string { return []string{binSh, "-c", script} }

var testEnv = Environment{ModelURL: "http://127.0.0.1:8081/v1", ToolsURL: "http://127.0.0.1:8082/mcp", NativeTools: modeNone, Port: "9000"}

func TestCardDeclaresTheHTTPJSONInterfaceAndTheProfile(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh("true")}, testEnv)
	resp, err := http.Get(ts.URL + "/.well-known/agent-card.json") //nolint:noctx // test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var card map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&card)
	ifaces := card["supportedInterfaces"].([]any)
	if ifaces[0].(map[string]any)["protocolBinding"] != "HTTP+JSON" {
		t.Fatalf("card = %v", card)
	}
	if !strings.Contains(toJSON(card), profileURI) {
		t.Fatalf("the card does not declare the turn profile: %v", card)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestATurnRunsTheCommandWithTheMeshEnvironment(t *testing.T) {
	cfg := Config{
		Argv: sh(`printf '%s|%s|%s|%s|%s|%s' "$KUBEMOOT_MODEL_URL" "$KUBEMOOT_TOOLS_URL" "$KUBEMOOT_TURN_KEY" "$KUBEMOOT_TIME_ZONE" "$TZ" "$MYKEY"`),
		Env:  []string{"MYKEY={{turnKey}}"},
	}
	_, ts := newTestServer(t, cfg, testEnv)
	code, r := post(t, ts.URL+"/message:send", body("what time is it?", map[string]any{"turnKey": "k-1", "timeZone": "America/New_York"}))
	if code != 200 || r.Task.Status.State != stateCompleted {
		t.Fatalf("code %d state %s: %s", code, r.Task.Status.State, r.text())
	}
	want := "http://127.0.0.1:8081/v1|http://127.0.0.1:8082/mcp|k-1|America/New_York|America/New_York|k-1"
	if r.text() != want || r.Task.Artifacts[0].Parts[0].Text != want {
		t.Fatalf("got %q want %q", r.text(), want)
	}
}

func TestTheCommandGetsAPrivateHomeThatIsRemoved(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	_, ts := newTestServer(t, Config{Argv: sh(`echo "$HOME" > ` + home + `; echo ok`)}, testEnv)
	post(t, ts.URL+"/message:send", body("q", nil))
	got, _ := os.ReadFile(home)
	dir := strings.TrimSpace(string(got))
	if dir == "" || dir == os.Getenv("HOME") {
		t.Fatalf("HOME = %q", dir)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("%s was not removed", dir)
	}
}

func TestTheQuestionAndInstructionsReachTheCommand(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh(`printf '%s' "$P"`), Env: []string{"P={{prompt}}"}}, testEnv)
	_, r := post(t, ts.URL+"/message:send", body("Is it Tuesday?", map[string]any{
		"instructions": "Be brief.", "now": "2026-10-20T10:00:00Z", "timeZone": "Mars/Base",
		"thread": []any{map[string]any{"agent": "a", "type": "agree", "content": "yes"}}}))
	for _, want := range []string{"Be brief.", "a (agree): yes", "2026-10-20T10:00:00Z", "Is it Tuesday?"} {
		if !strings.Contains(r.text(), want) {
			t.Errorf("prompt lacks %q: %q", want, r.text())
		}
	}
	if strings.Contains(r.text(), "Mars/Base") {
		t.Error("an unknown time zone reached the command")
	}
}

func TestACommandThatFailsIsAFailedTaskWithItsStderr(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh(`echo boom >&2; exit 3`)}, testEnv)
	_, r := post(t, ts.URL+"/message:send", body("q", nil))
	if r.Task.Status.State != stateFailed || !strings.Contains(r.text(), "boom") {
		t.Fatalf("state %s text %q", r.Task.Status.State, r.text())
	}
}

func TestACommandThatPrintsNothingIsAFailedTask(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh(`true`)}, testEnv)
	_, r := post(t, ts.URL+"/message:send", body("q", nil))
	if r.Task.Status.State != stateFailed {
		t.Fatalf("state %s", r.Task.Status.State)
	}
}

func TestACommandThatCannotStartIsAFailedTask(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: []string{"/nonexistent/agent"}}, testEnv)
	_, r := post(t, ts.URL+"/message:send", body("q", nil))
	if r.Task.Status.State != stateFailed {
		t.Fatalf("state %s", r.Task.Status.State)
	}
}

func TestTheDeadlineKillsTheCommandAndItsChildren(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pid")
	_, ts := newTestServer(t, Config{Argv: sh(`sleep 60 & echo $! > ` + pids + `; wait`)}, testEnv)
	start := time.Now()
	_, r := post(t, ts.URL+"/message:send", body("q", map[string]any{keyDeadline: 1}))
	if r.Task.Status.State != stateFailed || !strings.Contains(r.text(), "deadline") {
		t.Fatalf("state %s text %q", r.Task.Status.State, r.text())
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	assertGone(t, pids)
}

func assertGone(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}

func TestTheServerCapsTheSidecarsDeadline(t *testing.T) {
	turn, err := ParseTurn(body("q", map[string]any{keyDeadline: 9999}), 5*time.Second)
	if err != nil || turn.Deadline != 5*time.Second {
		t.Fatalf("%v %v", turn.Deadline, err)
	}
	turn, _ = ParseTurn(body("q", nil), time.Hour)
	if turn.Deadline != DefaultDeadline {
		t.Fatalf("default deadline = %v", turn.Deadline)
	}
}

func TestCancelTaskStopsARunningTurn(t *testing.T) {
	s, ts := newTestServer(t, Config{Argv: sh(`sleep 60`)}, testEnv)
	done := make(chan reply, 1)
	go func() { _, r := post(t, ts.URL+"/message:send", body("q", nil)); done <- r }()
	id := waitForTask(t, s)
	code, r := post(t, ts.URL+"/tasks/"+id+":cancel", nil)
	if code != 200 || r.Task.Status.State != stateCanceled {
		t.Fatalf("cancel: %d %s", code, r.Task.Status.State)
	}
	select {
	case got := <-done:
		if got.Task.Status.State != stateCanceled {
			t.Fatalf("the turn ended %s", got.Task.Status.State)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the canceled turn did not end")
	}
}

func waitForTask(t *testing.T, s *Server) string {
	t.Helper()
	for range 100 {
		s.mu.Lock()
		for id := range s.active {
			s.mu.Unlock()
			return id
		}
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no turn started")
	return ""
}

func TestCancelOfAnUnknownTaskOrOperationIs404(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh("true")}, testEnv)
	for _, p := range []string{"/tasks/nope:cancel", "/tasks/nope:subscribe", "/tasks/nope"} {
		if code, _ := post(t, ts.URL+p, nil); code != http.StatusNotFound {
			t.Errorf("%s = %d", p, code)
		}
	}
}

func TestAHungUpCallerKillsTheCommand(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pid")
	s, ts := newTestServer(t, Config{Argv: sh(`echo $$ > ` + pids + `; sleep 60`)}, testEnv)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/message:send", bytes.NewReader(body("q", nil)))
	go func() { _, _ = http.DefaultClient.Do(req) }() //nolint:bodyclose // the call is canceled
	waitForTask(t, s)
	time.Sleep(200 * time.Millisecond)
	cancel()
	assertGone(t, pids)
}

func TestAStuckTurnDoesNotBlockASecondOne(t *testing.T) {
	script := `if [ "$Q" = stuck ]; then sleep 60; else echo fast; fi`
	_, ts := newTestServer(t, Config{Argv: sh(script), Env: []string{"Q={{question}}"}, MaxConcurrent: 2}, testEnv)
	stuckDone := make(chan struct{})
	go func() {
		post(t, ts.URL+"/message:send", body("stuck", map[string]any{keyDeadline: 3}))
		close(stuckDone)
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	_, r := post(t, ts.URL+"/message:send", body("fast", nil))
	if r.Task.Status.State != stateCompleted || r.text() != "fast" || time.Since(start) > 2*time.Second {
		t.Fatalf("state %s text %q after %s", r.Task.Status.State, r.text(), time.Since(start))
	}
	<-stuckDone
}

func TestATurnOverTheLimitIsRejected(t *testing.T) {
	s, ts := newTestServer(t, Config{Argv: sh(`sleep 60`), MaxConcurrent: 1}, testEnv)
	go post(t, ts.URL+"/message:send", body("a", map[string]any{keyDeadline: 2}))
	waitForTask(t, s)
	_, r := post(t, ts.URL+"/message:send", body("b", nil))
	if r.Task.Status.State != stateRejected {
		t.Fatalf("state %s", r.Task.Status.State)
	}
}

func TestBadRequestsAreRefused(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh("echo x")}, testEnv)
	for name, b := range map[string][]byte{
		"not json": []byte("{"), "empty": []byte(""), "no text": body("", map[string]any{"turnKey": "k"}),
		"text is blank": body("  \n", nil),
	} {
		if code, _ := post(t, ts.URL+"/message:send", b); code != http.StatusBadRequest {
			t.Errorf("%s = %d", name, code)
		}
	}
}

func TestOnlyPostIsAcceptedOnSend(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh("echo x")}, testEnv)
	resp, err := http.Get(ts.URL + "/message:send") //nolint:noctx // test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
}

func TestArgvConditionsFollowTheNativeToolsMode(t *testing.T) {
	cfg := Config{Argv: []string{gooseCmd, subRun, "[none]--no-profile", "[declared,all]--with-builtin", "-t", promptTag}}
	cases := map[string][]string{
		modeNone:   {gooseCmd, subRun, "--no-profile", "-t", "pf"},
		"declared": {gooseCmd, subRun, "--with-builtin", "-t", "pf"},
		modeAll:    {gooseCmd, subRun, "--with-builtin", "-t", "pf"},
	}
	for mode, want := range cases {
		env := testEnv
		env.NativeTools = mode
		if got := NewRunner(cfg, env).Argv("pf"); !slices.Equal(got, want) {
			t.Errorf("%s: %v want %v", mode, got, want)
		}
	}
}

func TestModelHostDropsTheV1Suffix(t *testing.T) {
	r := NewRunner(Config{Argv: []string{"/x", "{{modelHost}}"}}, testEnv)
	if got := r.Argv("")[1]; got != "http://127.0.0.1:8081" {
		t.Fatalf("modelHost = %q", got)
	}
}

func TestEnvironmentDefaults(t *testing.T) {
	e := EnvironmentFrom(func(string) string { return "" })
	if e.NativeTools != "none" || e.Port != "9000" {
		t.Fatalf("%+v", e)
	}
	e = EnvironmentFrom(func(k string) string {
		return map[string]string{"KUBEMOOT_NATIVE_TOOLS": "all", "KUBEMOOT_TURN_PORT": "9100"}[k]
	})
	if e.NativeTools != "all" || e.Port != "9100" {
		t.Fatalf("%+v", e)
	}
}

func TestConfigValidation(t *testing.T) {
	for name, c := range map[string]Config{
		"no command": {}, "blank command": {Argv: []string{" "}}, "bad output": {Argv: []string{"x"}, Output: "xml"},
		"relative command": {Argv: []string{"goose"}}, "request value in argv": {Argv: []string{"/x", "{{turnKey}}"}},
		"negative": {Argv: []string{"x"}, MaxConcurrent: -1},
	} {
		if c.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	good := Config{Argv: []string{"/x"}, Output: OutputGooseJSON}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	if c, err := LoadConfig(write("ok.json", `{"argv":["/bin/goose"],"output":"goose-json","maxConcurrent":2}`)); err != nil || c.MaxConcurrent != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	for name, s := range map[string]string{"bad.json": `{`, "unknown.json": `{"argv":["/x"],"surprise":1}`, "empty.json": `{}`, "turnkey.json": `{"argv":["/x","--k={{turnKey}}"]}`} {
		if _, err := LoadConfig(write(name, s)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestExtract(t *testing.T) {
	good := `{"messages":[{"role":"user","content":[{"type":"text","text":"q"}]},
	  {"role":"assistant","content":[{"type":"text","text":"calling"}]},
	  {"role":"assistant","content":[{"type":"text","text":"The date is Tuesday."}]}],"metadata":{"status":"completed"}}`
	cases := []struct {
		name, kind, in, want string
		fails                bool
	}{
		{"goose last assistant text", OutputGooseJSON, good, "The date is Tuesday.", false},
		{"goose not completed", OutputGooseJSON, strings.Replace(good, "completed", "max_turns", 1), "", true},
		{"goose no assistant text", OutputGooseJSON, `{"messages":[{"role":"user","content":[]}]}`, "", true},
		{"goose not json", OutputGooseJSON, "hello", "", true},
		{"text trimmed", OutputText, "  hi \n", "hi", false},
		{"text empty", "", " \n", "", true},
	}
	for _, c := range cases {
		got, err := Extract(c.kind, c.in)
		if (err != nil) != c.fails || got != c.want {
			t.Errorf("%s: %q, %v", c.name, got, err)
		}
	}
}
