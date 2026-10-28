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
	// EngineDuckDB runs queries with DuckDB.
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
	if engine != EngineDuckDB {
		return nil, fmt.Errorf("%s=%q: want %s or %s", EnvEngine, engine, EngineNone, EngineDuckDB)
	}
	bin, err := exec.LookPath(orDefault(getenv(EnvDuckDBBin), "duckdb"))
	if err != nil {
		return nil, fmt.Errorf("%s=%s needs the duckdb program: %w", EnvEngine, engine, err)
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
		Store: fetcher, Dir: dir, Engine: DuckDB{Bin: bin}, Limits: lim, MaxArtifactBytes: int64(maxBytes),
	}, nil
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
