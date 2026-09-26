package materializer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/javajon/kubemoot/artifact-access/internal/artifact"
)

// testKey is a representative artifact object key reused across materializer tests.
const testKey = "crew/conv/thread/agent/tool-1"

// fakeStore serves in-memory bytes by key, or an error for missing keys.
type fakeStore struct {
	objects map[string]string
}

func (f *fakeStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	v, ok := f.objects[key]
	if !ok {
		return nil, errors.New("object not found: " + key)
	}
	return io.NopCloser(strings.NewReader(v)), nil
}

func TestMaterializeWritesFullContent(t *testing.T) {
	base := t.TempDir()
	key := "pilot/conv1/thread1/k8s-config/resources_list-0"
	payload := strings.Repeat("namespace,configmaps\n", 1000) // larger than one buffer
	m := &Materializer{Store: &fakeStore{objects: map[string]string{key: payload}}, BaseDir: base}

	path, err := m.Materialize(context.Background(), artifact.Reference{Key: key})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	want := filepath.Join(base, "pilot", "conv1", "thread1", "k8s-config", "resources_list-0")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read materialized: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	// The non-root sandbox container (uid 1000) sharing this volume must be able
	// to read the materialized file and traverse its directory; the sidecar writes
	// as root. Regression guard for the "Permission denied" on /artifacts bug.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat materialized: %v", err)
	}
	if fi.Mode().Perm()&0o004 == 0 {
		t.Fatalf("file not other-readable: mode %o", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm()&0o001 == 0 {
		t.Fatalf("dir not other-traversable: mode %o", di.Mode().Perm())
	}
}

func TestMaterializeIsIdempotent(t *testing.T) {
	base := t.TempDir()
	key := "c/x/y/a/t-0"
	store := &fakeStore{objects: map[string]string{key: "v1"}}
	m := &Materializer{Store: store, BaseDir: base}
	if _, err := m.Materialize(context.Background(), artifact.Reference{Key: key}); err != nil {
		t.Fatalf("first: %v", err)
	}
	store.objects[key] = "v2-updated"
	path, err := m.Materialize(context.Background(), artifact.Reference{Key: key})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2-updated" {
		t.Fatalf("expected overwrite, got %q", got)
	}
	// No leftover temp files.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".materialize-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestMaterializeRejectsUnsafeKey(t *testing.T) {
	m := &Materializer{Store: &fakeStore{objects: map[string]string{}}, BaseDir: t.TempDir()}
	if _, err := m.Materialize(context.Background(), artifact.Reference{Key: "../../etc/passwd"}); err != artifact.ErrUnsafeKey {
		t.Fatalf("expected ErrUnsafeKey, got %v", err)
	}
}

func TestMaterializeMissingObject(t *testing.T) {
	base := t.TempDir()
	m := &Materializer{Store: &fakeStore{objects: map[string]string{}}, BaseDir: base}
	if _, err := m.Materialize(context.Background(), artifact.Reference{Key: "c/x/y/a/t-0"}); err == nil {
		t.Fatal("expected error for missing object")
	}
	// A failed fetch must not leave a partial destination file behind.
	if _, err := os.Stat(filepath.Join(base, "c", "x", "y", "a", "t-0")); !os.IsNotExist(err) {
		t.Fatal("partial destination file should not exist after failed fetch")
	}
}

// flakyStore fails the first failCount Get calls, then serves payload — to drive
// the retry loop's transient-failure-then-recovery path.
type flakyStore struct {
	payload   string
	failCount int
	calls     int
}

func (f *flakyStore) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	f.calls++
	if f.calls <= f.failCount {
		return nil, errors.New("transient get failure")
	}
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

// errReader yields a few bytes then a non-EOF error, to exercise the io.Copy
// failure branch in fetchToTemp.
type errReader struct{ sent bool }

func (e *errReader) Read(p []byte) (int, error) {
	if e.sent {
		return 0, errors.New("mid-stream read error")
	}
	e.sent = true
	return copy(p, []byte("partial")), nil
}

// errStore serves a reader that fails partway through every stream.
type errStore struct{}

func (errStore) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(&errReader{}), nil
}

func TestMaterializeRecoversAfterTransientFailure(t *testing.T) {
	base := t.TempDir()
	key := testKey
	m := &Materializer{Store: &flakyStore{payload: "recovered", failCount: 1}, BaseDir: base}
	dest, err := m.Materialize(context.Background(), artifact.Reference{Key: key})
	if err != nil {
		t.Fatalf("expected recovery after one transient failure, got %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "recovered" {
		t.Errorf("content mismatch after recovery: %q", got)
	}
}

func TestMaterializeMkdirFailsWhenBaseDirIsAFile(t *testing.T) {
	// BaseDir is a regular file, so MkdirAll of the key's parent dir must fail.
	f, err := os.CreateTemp(t.TempDir(), "notadir-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	m := &Materializer{Store: &fakeStore{objects: map[string]string{}}, BaseDir: f.Name()}
	_, err = m.Materialize(context.Background(), artifact.Reference{Key: testKey})
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Fatalf("expected mkdir error when BaseDir is a file, got %v", err)
	}
}

func TestMaterializeStreamErrorLeavesNoDestFile(t *testing.T) {
	base := t.TempDir()
	key := testKey
	m := &Materializer{Store: errStore{}, BaseDir: base}
	_, err := m.Materialize(context.Background(), artifact.Reference{Key: key})
	if err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatalf("expected a stream-copy error, got %v", err)
	}
	dest, _ := artifact.LocalPath(base, key)
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("a failed stream must not leave a destination file")
	}
}

func TestLogLagHandlesAllTimestampForms(t *testing.T) {
	// logLag only logs; assert it walks all three branches without panicking.
	logLag(artifact.Reference{Key: "k"})                                               // empty CreatedAt -> early return
	logLag(artifact.Reference{Key: "k", CreatedAt: "not-a-timestamp"})                 // unparseable -> early return
	logLag(artifact.Reference{Key: "k", Bytes: 10, CreatedAt: "2026-06-27T01:50:01Z"}) // valid -> logs
}
