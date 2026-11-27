package confine

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	python = "python3"
	legacy = "/artifacts"
	runDir = "/t/r"
)

func TestCommandAndParseRoundTrip(t *testing.T) {
	spec := Spec{
		Mounts:      []Mount{{Path: "/kubemoot/workspace", Thread: "ns/crew/t1/"}, {Path: legacy}},
		RunDir:      "/tmp/code-sandbox-1",
		TempSize:    "64m",
		RequireProc: true,
		StatusFD:    3,
	}
	cmd := Command("/bin/self", spec, []string{python, "main.py"})
	if cmd[0] != "/bin/self" || cmd[1] != Arg {
		t.Fatalf("command starts %v", cmd[:2])
	}
	got, argv, err := parse(cmd[2:])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, spec) || !reflect.DeepEqual(argv, []string{python, "main.py"}) {
		t.Fatalf("parsed %+v %v", got, argv)
	}
}

func TestParseRefusesMalformedArguments(t *testing.T) {
	head := []string{flagTempSize, "1m", flagPrivate, runDir, flagStatus, "-1"}
	with := func(rest ...string) []string { return append(append([]string{}, head...), rest...) }
	for name, args := range map[string][]string{
		"no program":       with(flagEnd),
		"no separator":     with(python),
		"incomplete mount": with(flagMount, "/a"),
		"bad size":         {flagTempSize, "1m,uid=0", flagPrivate, runDir, flagStatus, "-1", flagEnd, python},
		"bad status":       {flagTempSize, "1m", flagPrivate, runDir, flagStatus, "x", flagEnd, python},
		"no run directory": {flagEnd, python},
		"empty":            {},
	} {
		if _, _, err := parse(args); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// TestFreshProcReportsOrFailsClosed needs a process that may not mount /proc, so it is skipped for root.
func TestFreshProcReportsOrFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root could mount /proc")
	}
	var report strings.Builder
	if err := freshProc(false, &report); err != nil || !strings.HasPrefix(report.String(), ProcNotReplaced) {
		t.Fatalf("a refused mount is reported and the run goes on: err %v report %q", err, report.String())
	}
	if err := freshProc(true, nil); err == nil {
		t.Fatal("a refused mount must fail when /proc is required")
	}
}

func TestAttrMapsTheCurrentUserOnly(t *testing.T) {
	a := Attr()
	if len(a.UidMappings) != 1 || a.UidMappings[0].Size != 1 || a.UidMappings[0].ContainerID != a.UidMappings[0].HostID {
		t.Fatalf("uid mapping %+v", a.UidMappings)
	}
	if a.GidMappingsEnableSetgroups {
		t.Fatal("setgroups must stay denied")
	}
}

func TestUnlistedWorkspaceMounts(t *testing.T) {
	info := `1 0 0:1 / / rw - overlay overlay rw
2 1 0:2 / /kubemoot/workspace rw - tmpfs tmpfs rw
3 1 0:3 / /artifacts rw - tmpfs tmpfs rw
4 1 0:4 / /data/workspace rw - tmpfs tmpfs rw
5 1 0:5 / /mnt/my\040data/artifacts rw - tmpfs tmpfs rw
6 1 0:6 / /workspace rw - tmpfs tmpfs rw
7 1 0:7 / /proc rw - proc proc rw
`
	got := UnlistedWorkspaceMounts(strings.NewReader(info), []string{"/kubemoot/workspace", legacy})
	want := []string{"/data/workspace", "/mnt/my data/artifacts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := UnlistedWorkspaceMounts(strings.NewReader(info), append([]string{"/data/workspace", "/mnt/my data/artifacts", legacy},
		"/kubemoot/workspace")); len(got) != 0 {
		t.Fatalf("listed mounts are fine: %v", got)
	}
}

// An unprivileged test process cannot mount /proc, which is the refusal freshProc meets on a host
// that masks it. Run as root the mount would succeed on the test host itself, so the test does not.
func TestProcThatCannotBeReplacedFailsWhenRequiredAndIsReportedOtherwise(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the /proc mount would succeed on the test host")
	}
	if err := freshProc(true, nil); err == nil || !strings.Contains(err.Error(), "replace /proc") {
		t.Fatalf("required: got %v, want a replace /proc error", err)
	}
	var report strings.Builder
	if err := freshProc(false, &report); err != nil {
		t.Fatalf("not required: got %v, want the run to go on", err)
	}
	if !strings.HasPrefix(report.String(), ProcNotReplaced+": ") {
		t.Fatalf("report %q, want it to start with %q", report.String(), ProcNotReplaced)
	}
}
