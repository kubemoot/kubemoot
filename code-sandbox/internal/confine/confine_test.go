package confine

import (
	"reflect"
	"testing"
)

const python = "python3"

func TestCommandAndParseRoundTrip(t *testing.T) {
	mounts := []Mount{{Path: "/kubemoot/workspace", View: "/kubemoot/workspace/ns/crew/t1"}, {Path: "/artifacts"}}
	cmd := Command("/bin/self", mounts, []string{python, "main.py"})
	if cmd[0] != "/bin/self" || cmd[1] != Arg {
		t.Fatalf("command starts %v", cmd[:2])
	}
	gotMounts, argv, err := parse(cmd[2:])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotMounts, mounts) || !reflect.DeepEqual(argv, []string{python, "main.py"}) {
		t.Fatalf("parsed %v %v", gotMounts, argv)
	}
}

func TestParseRefusesMalformedArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no program":       {flagEnd},
		"no separator":     {python},
		"incomplete mount": {flagMount, "/a"},
		"empty":            {},
	} {
		if _, _, err := parse(args); err == nil {
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
