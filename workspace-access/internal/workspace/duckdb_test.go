package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const engineDuckDB = "duckdb"

// duckBin returns the duckdb program the DuckDB tests run, or skips them when none
// is installed. Set REQUIRE_DUCKDB=1 to fail instead of skip.
func duckBin(t *testing.T) string {
	t.Helper()
	name := "duckdb"
	if v := os.Getenv("ARTIFACT_DUCKDB_BIN"); v != "" {
		name = v
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("REQUIRE_DUCKDB") == "1" {
			t.Fatalf("duckdb program required: %v", err)
		}
		t.Skip("no duckdb program on PATH; DuckDB tests skipped")
	}
	return bin
}

// DuckDB reads files and addresses by name in functions; these tests show it cannot.
func TestQueryRefusesFilesOutsideTheNamedArtifacts(t *testing.T) {
	s := newService(t, engineDuckDB, map[string]string{"k": jsonOneA, "other": `[{"secret":1}]`})
	otherPath := filepath.Join(s.Dir, "other")
	for _, sql := range []string{
		"SELECT * FROM read_text('/etc/passwd')",
		"SELECT * FROM read_json_auto('" + otherPath + "')",
		"SELECT * FROM glob('/*')",
	} {
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%q: want ErrRefused, got %v", sql, err)
		}
	}
}

func TestQueryRefusesTheNetwork(t *testing.T) {
	s := newService(t, engineDuckDB, map[string]string{"k": jsonOneA})
	for _, sql := range []string{
		"SELECT * FROM read_csv('http://127.0.0.1:9/x.csv')",
		"SELECT * FROM read_parquet('https://example.com/x.parquet')",
		"SELECT * FROM read_json('s3://bucket/key.json')",
	} {
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%q: want ErrRefused, got %v", sql, err)
		}
	}
}

func TestQueryDoesNotDownloadExtensions(t *testing.T) {
	s := newService(t, engineDuckDB, map[string]string{"k": jsonOneA})
	_, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT * FROM read_markdown('/x.md')")
	if err == nil {
		t.Fatal("a function from an extension that is not built in must fail, not download")
	}
}
