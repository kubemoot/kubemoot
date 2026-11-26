package tokenstore_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBao is an OpenBao KV v2 server in memory: versions, check-and-set, delete_version_after
// and listing, with a clock the test moves.
type fakeBao struct {
	mu      sync.Mutex
	token   string
	now     time.Time
	secrets map[string]*baoSecret
	calls   int
}

type baoSecret struct {
	version  uint64
	value    string
	ttl      time.Duration
	written  time.Time
	hasValue bool
}

func newFakeBao(t *testing.T, token string) (*fakeBao, *httptest.Server) {
	t.Helper()
	f := &fakeBao{token: token, now: time.Unix(1_800_000_000, 0), secrets: map[string]*baoSecret{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeBao) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// live drops an expired value. The caller holds the lock.
func (f *fakeBao) live(path string) *baoSecret {
	s := f.secrets[path]
	if s != nil && s.hasValue && s.ttl > 0 && !f.now.Before(s.written.Add(s.ttl)) {
		s.hasValue = false
	}
	return s
}

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if r.Header.Get("X-Vault-Token") != f.token {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/"), "/", 3)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	f.route(w, r, parts[1], parts[2], raw)
}

const (
	kindData     = "data"
	kindMetadata = "metadata"
)

func (f *fakeBao) route(w http.ResponseWriter, r *http.Request, kind, path string, raw []byte) {
	switch r.Method + " " + kind {
	case "GET " + kindData:
		f.get(w, path)
	case "POST " + kindData:
		f.put(w, path, raw)
	case "POST " + kindMetadata:
		f.setTTL(w, path, raw)
	case "DELETE " + kindMetadata:
		delete(f.secrets, path)
		w.WriteHeader(http.StatusNoContent)
	case "LIST " + kindMetadata:
		f.list(w, path)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeBao) get(w http.ResponseWriter, path string) {
	s := f.live(path)
	if s == nil || !s.hasValue {
		http.Error(w, `{"errors":[]}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{kindData: map[string]any{
		kindData: map[string]string{"v": s.value}, kindMetadata: map[string]any{"version": s.version}}})
}

func (f *fakeBao) setTTL(w http.ResponseWriter, path string, raw []byte) {
	var body struct {
		After string `json:"delete_version_after"`
	}
	if json.Unmarshal(raw, &body) != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	d, err := time.ParseDuration(body.After)
	if err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	s := f.secrets[path]
	if s == nil {
		s = &baoSecret{}
		f.secrets[path] = s
	}
	s.ttl = d
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeBao) put(w http.ResponseWriter, path string, raw []byte) {
	var body struct {
		Options *struct {
			CAS uint64 `json:"cas"`
		} `json:"options"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	s := f.live(path)
	if s == nil {
		s = &baoSecret{}
		f.secrets[path] = s
	}
	current := uint64(0)
	if s.hasValue {
		current = s.version
	}
	if body.Options != nil && body.Options.CAS != current {
		http.Error(w, `{"errors":["check-and-set parameter did not match the current version"]}`, http.StatusBadRequest)
		return
	}
	s.version++
	s.value, s.written, s.hasValue = body.Data["v"], f.now, true
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": s.version}})
}

func (f *fakeBao) list(w http.ResponseWriter, path string) {
	prefix := strings.TrimSuffix(path, "/") + "/"
	var keys []string
	for p := range f.secrets {
		if strings.HasPrefix(p, prefix) {
			if s := f.live(p); s != nil && s.hasValue {
				keys = append(keys, strings.TrimPrefix(p, prefix))
			}
		}
	}
	if len(keys) == 0 {
		http.Error(w, `{"errors":[]}`, http.StatusNotFound)
		return
	}
	sort.Strings(keys)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": keys}})
}
