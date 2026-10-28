package workspace

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// Environment variables that configure the query tool.
const (
	EnvEngine           = "ARTIFACT_QUERY_ENGINE"
	EnvDuckDBBin        = "ARTIFACT_DUCKDB_BIN"
	EnvTimeout          = "ARTIFACT_QUERY_TIMEOUT"
	EnvMaxRows          = "ARTIFACT_QUERY_MAX_ROWS"
	EnvMemory           = "ARTIFACT_QUERY_MEMORY"
	EnvMaxArtifactBytes = "ARTIFACT_QUERY_MAX_ARTIFACT_BYTES"

	// EngineNone is the default: the query tool is not offered.
	EngineNone = "none"
	// EngineSQLite runs queries with SQLite inside the artifact-access process.
	EngineSQLite = "sqlite"
	// EngineDuckDB runs queries with DuckDB in a separate program.
	EngineDuckDB = "duckdb"

	defaultTimeout          = 10 * time.Second
	defaultMaxRows          = 100
	defaultMemory           = "256MB"
	defaultMaxArtifactBytes = 32 << 20
)

// FromEnv builds the query service the environment asks for. It returns nil and no
// error when the engine is "none" or unset, so the tool is not offered and the
// server behaves as it did before. An unknown engine, an unusable limit, or an
// engine program that is missing is an error.
func FromEnv(getenv func(string) string, fetcher store.Getter, dir string) (*Service, error) {
	engine := orDefault(getenv(EnvEngine), EngineNone)
	if engine == EngineNone {
		return nil, nil
	}
	eng, err := newEngine(engine, getenv)
	if err != nil {
		return nil, err
	}
	lim, err := limitsFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	maxBytes, err := positiveInt(getenv, EnvMaxArtifactBytes, defaultMaxArtifactBytes)
	if err != nil {
		return nil, err
	}
	return &Service{
		Store: fetcher, Dir: dir, Engine: eng, Limits: lim, MaxArtifactBytes: int64(maxBytes),
	}, nil
}

// newEngine builds the named engine. SQLite is part of this program; DuckDB needs its
// program on the image.
func newEngine(engine string, getenv func(string) string) (Engine, error) {
	switch engine {
	case EngineSQLite:
		return SQLite{}, nil
	case EngineDuckDB:
		bin, err := exec.LookPath(orDefault(getenv(EnvDuckDBBin), "duckdb"))
		if err != nil {
			return nil, fmt.Errorf("%s=%s needs the duckdb program: %w", EnvEngine, engine, err)
		}
		return DuckDB{Bin: bin}, nil
	}
	return nil, fmt.Errorf("%s=%q: want %s, %s or %s", EnvEngine, engine, EngineNone, EngineSQLite, EngineDuckDB)
}

func limitsFromEnv(getenv func(string) string) (Limits, error) {
	timeout := defaultTimeout
	if v := getenv(EnvTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Limits{}, fmt.Errorf("%s=%q: want a positive duration such as 10s", EnvTimeout, v)
		}
		timeout = d
	}
	rows, err := positiveInt(getenv, EnvMaxRows, defaultMaxRows)
	if err != nil {
		return Limits{}, err
	}
	return Limits{Timeout: timeout, MaxRows: rows, MemoryLimit: orDefault(getenv(EnvMemory), defaultMemory)}, nil
}

func positiveInt(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s=%q: want a positive whole number", key, v)
	}
	return n, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
