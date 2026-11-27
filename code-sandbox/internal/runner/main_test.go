package runner

import (
	"os"
	"testing"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/confine"
)

// TestMain lets the test binary act as the confining helper a run re-executes.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == confine.Arg {
		confine.Main(os.Args[2:])
	}
	os.Exit(m.Run())
}
