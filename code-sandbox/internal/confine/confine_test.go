package confine

import (
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
	mounts := []Mount{{Path: "/kubemoot/workspace", View: "/kubemoot/workspace/ns/crew/t1"}, {Path: legacy}}
	cmd := Command("/bin/self", mounts, "/tmp/code-sandbox-1", "64m", []string{python, "main.py"})
	if cmd[0] != "/bin/self" || cmd[1] != Arg {
		t.Fatalf("command starts %v", cmd[:2])
	}
	p, err := parse(cmd[2:])
	if err != nil {
		t.Fatal(err)
	}
	if p.runDir != "/tmp/code-sandbox-1" || p.tempSize != "64m" || !reflect.DeepEqual(p.mounts, mounts) ||
		!reflect.DeepEqual(p.argv, []string{python, "main.py"}) {
		t.Fatalf("parsed %+v", p)
	}
}

func TestParseRefusesMalformedArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no program":       {flagTempSize, "1m", flagPrivate, runDir, flagEnd},
		"no separator":     {flagTempSize, "1m", flagPrivate, runDir, python},
		"incomplete mount": {flagTempSize, "1m", flagPrivate, runDir, flagMount, "/a"},
		"bad size":         {flagTempSize, "1m,uid=0", flagPrivate, runDir, flagEnd, python},
		"no run directory": {flagEnd, python},
		"empty":            {},
	} {
		if _, err := parse(args); err == nil {
			t.Errorf("%s: want an error", name)
		}
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
