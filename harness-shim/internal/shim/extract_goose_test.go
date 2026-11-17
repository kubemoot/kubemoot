package shim

import (
	"os"
	"strings"
	"testing"
)

func capture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Both captures are the real stdout of `goose run --output-format json` (goose 1.55.0, the image's
// own command) with a model door that refused the call, one without -q and one with it.
const (
	gooseBanner = "goose-1.55.0-json-with-banner.txt"
	gooseQuiet  = "goose-1.55.0-json-quiet.txt"
)

func TestTheBannerGooseCapturePrintsBeforeItsJSON(t *testing.T) {
	out := capture(t, gooseBanner)
	if !strings.HasPrefix(strings.TrimSpace(out), "__(") {
		t.Fatalf("the capture no longer starts with the banner: %q", out[:40])
	}
}

func TestExtractGooseReadsPastTheBannerAndNamesTheError(t *testing.T) {
	for _, name := range []string{gooseBanner, gooseQuiet} {
		_, err := Extract(OutputGooseJSON, capture(t, name))
		if err == nil || strings.Contains(err.Error(), "no JSON result") ||
			!strings.Contains(err.Error(), `status "error"`) || !strings.Contains(err.Error(), "403 Forbidden") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The success captures are the real ones with the failed assistant message replaced by an answer
// and the status set to completed.
func TestExtractGooseTakesTheAnswerWithOrWithoutTheBanner(t *testing.T) {
	for _, name := range []string{gooseBanner, gooseQuiet} {
		out := capture(t, name)
		i := strings.Index(out, `"type": "error"`)
		j := strings.Index(out[i:], "}")
		out = out[:i] + `"type": "text", "text": "Hello"` + out[i+j:]
		out = strings.Replace(out, `"status": "error"`, `"status": "completed"`, 1)
		if got, err := Extract(OutputGooseJSON, out); err != nil || got != "Hello" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestExtractGooseStillRefusesOutputWithNoJSON(t *testing.T) {
	for _, out := range []string{"", "__( O)> goose is ready\nno result", "{ not json"} {
		if _, err := Extract(OutputGooseJSON, out); err == nil {
			t.Errorf("%q: want an error", out)
		}
	}
}

func TestTheGooseConfigurationRunsQuiet(t *testing.T) {
	cfg, err := LoadConfig("../../goose/config.json")
	if err != nil {
		t.Fatal(err)
	}
	argv := NewRunner(cfg, testEnv).Argv("pf")
	if len(argv) < 3 || argv[1] != "run" || argv[2] != "-q" {
		t.Errorf("goose run must be quiet so stdout is only the result: %v", argv)
	}
}
