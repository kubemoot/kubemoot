package shim

import (
	"slices"
	"testing"
)

// The goose image's own configuration loads, and its command reads as goose should be asked.
func TestTheGooseConfigurationIsValidAndFollowsNativeTools(t *testing.T) {
	cfg, err := LoadConfig("../../goose/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Output != OutputGooseJSON {
		t.Fatalf("output = %q", cfg.Output)
	}
	turn := Turn{Question: "q", TurnKey: "k"}
	none := NewRunner(cfg, testEnv).Argv(turn)
	if !slices.Contains(none, "--no-profile") {
		t.Errorf("nativeTools none must pass --no-profile: %v", none)
	}
	if i := slices.Index(none, "--with-streamable-http-extension"); i < 0 || none[i+1] != testEnv.ToolsURL {
		t.Errorf("the tools door is not goose's MCP extension: %v", none)
	}
	for _, mode := range []string{"declared", modeAll} {
		env := testEnv
		env.NativeTools = mode
		if slices.Contains(NewRunner(cfg, env).Argv(turn), "--no-profile") {
			t.Errorf("nativeTools %s still passes --no-profile", mode)
		}
	}
	envs := NewRunner(cfg, testEnv).Environ(turn, "/tmp/h")
	for _, want := range []string{"OPENAI_HOST=http://127.0.0.1:8081", "OPENAI_API_KEY=k", "GOOSE_PROVIDER=openai", "OPENAI_BASE_PATH=v1/chat/completions"} {
		if !slices.Contains(envs, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
}
