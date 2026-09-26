// Package materializer streams a referenced object from an ObjectStore to a
// local file under a base directory, so the no-network sandbox can read it.
//
// The ObjectStore dependency is an interface so the core streaming/path logic is
// unit-tested without a live NATS (see materializer_test.go); the real
// NATS-backed implementation lives in internal/natsstore.
package materializer

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/javajon/kubemoot/artifact-access/internal/artifact"
	"github.com/javajon/kubemoot/artifact-access/internal/store"
)

// Diagnostic retry around the object read (artifact-corruption hunt). If a read fails
// (e.g. "digests do not match"), retry a few times: a recovery means the failure was
// transient and the fix is a retry; a permanent failure across all attempts is logged
// so the next fitness run pins transient-vs-permanent. Remove the ARTIFACT-DIAG logging
// once the root cause is found; the retry itself is worth keeping.
const (
	maxFetchAttempts = 3
	fetchBackoff     = 150 * time.Millisecond
)

// Materializer writes referenced objects to files under BaseDir. The object source
// is the shared store.Getter (single source of truth for the fetch contract).
type Materializer struct {
	Store   store.Getter
	BaseDir string
}

// Materialize streams the referenced object to a local file and returns the
// path. The write is atomic (temp file + rename) so a reader never sees a
// partial file. Re-materializing the same key is idempotent (overwrites). The fetch
// is retried on failure to survive (and characterize) the intermittent NATS read
// corruption seen under load.
func (m *Materializer) Materialize(ctx context.Context, ref artifact.Reference) (string, error) {
	dest, err := artifact.LocalPath(m.BaseDir, ref.Key)
	if err != nil {
		return "", err
	}
	// 0o755 dirs (and 0o644 file below): the sidecar writes as root but the
	// sandbox container that reads the artifact runs as a non-root user (uid 1000)
	// in the same pod, so the tree must be traversable/readable by "other".
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("mkdir for %s: %w", ref.Key, err)
	}
	logLag(ref)

	var tmpName string
	var lastErr error
	for attempt := 1; attempt <= maxFetchAttempts; attempt++ {
		tmpName, lastErr = m.fetchToTemp(ctx, ref, filepath.Dir(dest))
		if lastErr == nil {
			if attempt > 1 {
				log.Printf("ARTIFACT-DIAG materialize RECOVERED key=%s after %d attempts (transient)", ref.Key, attempt)
			}
			break
		}
		log.Printf("ARTIFACT-DIAG materialize attempt %d/%d FAILED key=%s bytes=%d: %v",
			attempt, maxFetchAttempts, ref.Key, ref.Bytes, lastErr)
		if attempt < maxFetchAttempts {
			time.Sleep(fetchBackoff * time.Duration(attempt))
		}
	}
	if lastErr != nil {
		log.Printf("ARTIFACT-DIAG materialize PERMANENT-FAIL key=%s bytes=%d after %d attempts: %v",
			ref.Key, ref.Bytes, maxFetchAttempts, lastErr)
		return "", lastErr
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("rename for %s: %w", ref.Key, err)
	}
	return dest, nil
}

// fetchToTemp streams the object into a fresh temp file (0o644) and returns its path.
// On any failure it removes the temp so a retry starts clean. The "bytes copied"
// count in the error tells us whether all chunks arrived before the digest failed.
func (m *Materializer) fetchToTemp(ctx context.Context, ref artifact.Reference, destDir string) (string, error) {
	rc, err := m.Store.Get(ctx, ref.Key)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", ref.Key, err)
	}
	defer func() { _ = rc.Close() }()

	tmp, err := os.CreateTemp(destDir, ".materialize-*")
	if err != nil {
		return "", fmt.Errorf("temp file for %s: %w", ref.Key, err)
	}
	tmpName := tmp.Name()

	n, err := io.Copy(tmp, rc)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("stream %s (%d bytes copied): %w", ref.Key, n, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("close temp for %s: %w", ref.Key, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("chmod for %s: %w", ref.Key, err)
	}
	return tmpName, nil
}

// logLag records how long after the producer wrote the object the materializer read
// it - "the lag" - which may correlate with the corruption.
func logLag(ref artifact.Reference) {
	if ref.CreatedAt == "" {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, ref.CreatedAt)
	if err != nil {
		return
	}
	log.Printf("ARTIFACT-DIAG materialize start key=%s bytes=%d lagMs=%d",
		ref.Key, ref.Bytes, time.Since(t).Milliseconds())
}
