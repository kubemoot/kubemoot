package runner

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The programs agents write run on the Python and the tools of the image. The image is built
// by Paketo buildpacks from image/ (Python from BP_CPYTHON_VERSION in image/buildpacks.env);
// these tests state what a sandboxed program must find there, and hold the interpreter the
// tests run on to the same minor version, so a change of either side fails here.
//
// The contract is what the Dockerfile image gave: Python with the standard library only
// (python:3-slim carries pip as its only package), bash, and GNU date with the zone database
// for time-zone conversion and date arithmetic.

// pinnedPythonMinor is the "3.14" of BP_CPYTHON_VERSION=3.14.* in image/buildpacks.env.
func pinnedPythonMinor(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../image/buildpacks.env")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^BP_CPYTHON_VERSION=(\d+\.\d+)\.\*$`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("image/buildpacks.env has no BP_CPYTHON_VERSION=MAJOR.MINOR.*")
	}
	return m[1]
}

func TestSandboxPythonIsThePinnedMinorVersion(t *testing.T) {
	res := execute(t, newRunner(t), "python", "import sys\nprint('%d.%d' % sys.version_info[:2])")
	got := strings.TrimSpace(res.Stdout)
	if want := pinnedPythonMinor(t); got != want {
		t.Fatalf("sandboxed code runs Python %s, but image/buildpacks.env pins %s", got, want)
	}
}

// Only the standard library: no package an agent could import beyond it. The packaging tools
// an interpreter ships with (pip, and on some builds setuptools and wheel) are not packages
// agents use; anything else, numpy for example, would mean the image installs more than the
// Dockerfile image did.
func TestSandboxPythonHasNoThirdPartyPackages(t *testing.T) {
	code := "import json\nfrom importlib import metadata\n" +
		"print(json.dumps(sorted({d.metadata['Name'].lower() for d in metadata.distributions()})))"
	res := execute(t, newRunner(t), "python", code)
	var names []string
	if err := json.Unmarshal([]byte(res.Stdout), &names); err != nil {
		t.Fatalf("package list %q: %v (stderr %q)", res.Stdout, err, res.Stderr)
	}
	for _, name := range names {
		if !slices.Contains([]string{"pip", "setuptools", "wheel"}, name) {
			t.Errorf("sandboxed code finds the package %q; the image holds the standard library only (got %v)", name, names)
		}
	}
}

// The standard library modules agents use are all there (a Python without ssl or zoneinfo
// would still start).
func TestSandboxPythonHasTheStandardLibraryAgentsUse(t *testing.T) {
	modules := []string{"json", "csv", "sqlite3", "zoneinfo", "datetime", "decimal", "statistics", "re", "urllib.request", "ssl", "zlib", "hashlib"}
	res := execute(t, newRunner(t), "python", "import importlib\nfor m in "+pyList(modules)+":\n    importlib.import_module(m)\nprint('ok')")
	if strings.TrimSpace(res.Stdout) != "ok" {
		t.Fatalf("a standard library module is missing: stdout %q stderr %q", res.Stdout, res.Stderr)
	}
}

func pyList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "'" + s + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// Time-zone conversion and date arithmetic with GNU date, which the sandbox documents.
func TestSandboxDateConvertsZonesAndDoesArithmetic(t *testing.T) {
	cases := map[string]string{
		"TZ=Asia/Tokyo date -d '2026-11-20 15:00 EST' '+%F %R %Z'": "2026-11-21 05:00 JST",
		"date -d '2026-11-20 + 45 days' +%F":                       "2027-01-04",
	}
	for code, want := range cases {
		res := execute(t, newRunner(t), "bash", code)
		if got := strings.TrimSpace(res.Stdout); got != want {
			t.Errorf("%s printed %q (stderr %q), want %q", code, got, res.Stderr, want)
		}
	}
}

// The image starts the server under tini through the Paketo launcher, with the tini
// buildpack on and the Python buildpacks given a requirements file.
func TestImageFilesStartTheServerUnderTini(t *testing.T) {
	procfile, err := os.ReadFile("../../image/Procfile")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(procfile)); got != "web: tini -s -- /workspace/code-sandbox" {
		t.Errorf("Procfile = %q", got)
	}
	env, err := os.ReadFile("../../image/buildpacks.env")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(env), "\n")
	for _, want := range []string{"BP_LAUNCH_WITH_TINI=true", "BP_DIRECT_PROCESS=true"} {
		if !slices.Contains(lines, want) {
			t.Errorf("image/buildpacks.env lacks %s", want)
		}
	}
	if _, err := os.Stat("../../image/requirements.txt"); err != nil {
		t.Errorf("the Python buildpacks need image/requirements.txt: %v", err)
	}
}
