package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/kubemoot/kubemoot/artifact-access/internal/artifact"
	"github.com/kubemoot/kubemoot/artifact-access/internal/materializer"
	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

const (
	// MaxArtifacts is the most artifacts one query may name.
	MaxArtifacts = 8
	// workDirPrefix names the per-query directory for converted copies; it is removed
	// when the query ends.
	workDirPrefix = ".query-"
)

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// Service runs queries over the artifacts of a discussion.
type Service struct {
	// Store fetches an artifact that is not yet a file under Dir.
	Store store.Getter
	// Dir is the artifact directory (/artifacts), where artifacts are materialized.
	Dir    string
	Engine Engine
	Limits Limits
	// MaxArtifactBytes is the largest artifact a query may read.
	MaxArtifactBytes int64
}

// Query runs sql over the named artifacts and returns the rendered rows. artifacts
// maps the table name the SQL uses to the artifact's object key.
func (s *Service) Query(ctx context.Context, artifacts map[string]string, sql string) (string, error) {
	if err := checkRequest(artifacts, sql); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", fmt.Errorf("artifact directory: %w", err)
	}
	workDir, err := os.MkdirTemp(s.Dir, workDirPrefix)
	if err != nil {
		return "", fmt.Errorf("work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	rels, err := s.relations(ctx, artifacts, workDir)
	if err != nil {
		return "", err
	}
	res, err := s.Engine.Query(ctx, rels, sql, s.Limits)
	if err != nil {
		return "", err
	}
	return Render(res), nil
}

func checkRequest(artifacts map[string]string, sql string) error {
	if strings.TrimSpace(sql) == "" {
		return errors.New("sql is required")
	}
	if len(artifacts) == 0 {
		return errors.New("artifacts is required: map each table name to an artifact key")
	}
	if len(artifacts) > MaxArtifacts {
		return fmt.Errorf("at most %d artifacts per query, got %d", MaxArtifacts, len(artifacts))
	}
	for name := range artifacts {
		if !tableName.MatchString(name) {
			return fmt.Errorf("table name %q must be letters, digits and underscores, not starting with a digit", name)
		}
	}
	return nil
}

// relations makes one Relation per named artifact, in name order.
func (s *Service) relations(ctx context.Context, artifacts map[string]string, workDir string) ([]Relation, error) {
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	rels := make([]Relation, 0, len(names))
	for _, name := range names {
		rel, err := s.relation(ctx, name, artifacts[name], workDir)
		if err != nil {
			return nil, fmt.Errorf("artifact %q: %w", name, err)
		}
		rels = append(rels, rel)
	}
	return rels, nil
}

// relation finds the artifact's file under Dir, fetching it first when it is not
// there yet, and prepares it for the engine.
func (s *Service) relation(ctx context.Context, name, key, workDir string) (Relation, error) {
	path, err := s.materialized(ctx, key)
	if err != nil {
		return Relation{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Relation{}, err
	}
	if s.MaxArtifactBytes > 0 && info.Size() > s.MaxArtifactBytes {
		return Relation{}, fmt.Errorf("the artifact is %d bytes, over the %d byte limit for queries",
			info.Size(), s.MaxArtifactBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Relation{}, err
	}
	rel, _, err := Prepare(name, path, data, workDir)
	return rel, err
}

// materialized returns the local file for key, materializing it when absent.
func (s *Service) materialized(ctx context.Context, key string) (string, error) {
	path, err := artifact.LocalPath(s.Dir, key)
	if err != nil {
		return "", fmt.Errorf("bad key %q: %w", key, err)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path, nil
	}
	m := &materializer.Materializer{Store: s.Store, BaseDir: s.Dir}
	path, err = m.Materialize(ctx, artifact.Reference{Key: key})
	if err != nil {
		return "", fmt.Errorf("not found or unreadable (%s): %w", key, err)
	}
	return path, nil
}
