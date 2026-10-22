// Package shim is the harness-agnostic A2A face for command-line agents. It serves the agent card and
// SendMessage that the Moot Mesh sidecar calls, runs a configured command once per turn with the
// model URL, tools URL, turn key and requester time zone in its environment, and returns what the
// command printed. The command is configuration, so one shim serves goose, opencode, or `claude -p`.
package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Defaults for a Config that leaves a field out.
const (
	DefaultDeadline      = 300 * time.Second
	DefaultMaxConcurrent = 4
	OutputText           = "text"
	OutputGooseJSON      = "goose-json"
)

// Config is the shim's configuration file.
type Config struct {
	// Name is the agent card's name.
	Name string `json:"name"`
	// Argv is the command and its arguments. Elements may use only {{promptFile}}, {{toolsURL}},
	// {{modelURL}}, {{modelHost}} and {{nativeTools}}, and argv[0] is an absolute path. An element
	// that starts with [mode,mode] is kept only when KUBEMOOT_NATIVE_TOOLS is one of the modes.
	Argv []string `json:"argv"`
	// Env is extra KEY=VALUE entries for the command, expanded like Argv; they may also use
	// {{turnKey}}, {{timeZone}}, {{now}}, {{prompt}}, {{question}} and {{instructions}}.
	Env []string `json:"env"`
	// Output names how the command's stdout becomes the answer: text or goose-json.
	Output string `json:"output"`
	// MaxDeadlineSeconds caps the per-turn deadline the sidecar sends; 0 means the cap is 1 hour.
	MaxDeadlineSeconds int `json:"maxDeadlineSeconds"`
	// MaxConcurrent is the number of turns that may run at once; a turn over the limit is rejected.
	MaxConcurrent int `json:"maxConcurrent"`
}

// Environment is what the pod gives the shim; the operator sets these on the harness container.
type Environment struct {
	ModelURL    string
	ToolsURL    string
	NativeTools string
	Port        string
}

// EnvironmentFrom reads the Moot Mesh variables through getenv.
func EnvironmentFrom(getenv func(string) string) Environment {
	e := Environment{
		ModelURL: getenv("KUBEMOOT_MODEL_URL"), ToolsURL: getenv("KUBEMOOT_TOOLS_URL"),
		NativeTools: getenv("KUBEMOOT_NATIVE_TOOLS"), Port: getenv("KUBEMOOT_TURN_PORT"),
	}
	if e.NativeTools == "" {
		e.NativeTools = "none"
	}
	if e.Port == "" {
		e.Port = "9000"
	}
	return e
}

// LoadConfig reads and validates a configuration file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own flag
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.Validate()
}

// Validate reports why a configuration cannot run turns.
func (c *Config) Validate() error {
	var errs []error
	errs = append(errs, c.validateArgv()...)
	switch c.Output {
	case "", OutputText, OutputGooseJSON:
	default:
		errs = append(errs, fmt.Errorf("output %q is not %s or %s", c.Output, OutputText, OutputGooseJSON))
	}
	if c.MaxConcurrent < 0 || c.MaxDeadlineSeconds < 0 {
		errs = append(errs, errors.New("maxConcurrent and maxDeadlineSeconds cannot be negative"))
	}
	return errors.Join(errs...)
}

// requestPlaceholders carry values derived from the request; only env entries may use them.
var requestPlaceholders = []string{
	"{{prompt}}", "{{question}}", "{{instructions}}", "{{turnKey}}", "{{timeZone}}", "{{now}}",
}

func (c *Config) validateArgv() []error {
	if len(c.Argv) == 0 || strings.TrimSpace(c.Argv[0]) == "" {
		return []error{errors.New("argv names no command")}
	}
	var errs []error
	if !filepath.IsAbs(c.Argv[0]) {
		errs = append(errs, fmt.Errorf("argv[0] %q is not an absolute path", c.Argv[0]))
	}
	for i, a := range c.Argv {
		for _, p := range requestPlaceholders {
			if strings.Contains(a, p) {
				errs = append(errs, fmt.Errorf("argv[%d] uses %s, a request-derived value; only env entries may", i, p))
			}
		}
	}
	return errs
}

func (c *Config) concurrency() int {
	if c.MaxConcurrent > 0 {
		return c.MaxConcurrent
	}
	return DefaultMaxConcurrent
}

func (c *Config) maxDeadline() time.Duration {
	if c.MaxDeadlineSeconds > 0 {
		return time.Duration(c.MaxDeadlineSeconds) * time.Second
	}
	return time.Hour
}

func (c *Config) cardName() string {
	if c.Name != "" {
		return c.Name
	}
	return "harness-shim"
}
