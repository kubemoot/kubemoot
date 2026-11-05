package colname

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// fixture is the cases shared with agent-runtime's ColumnNamesTest.
type fixture struct {
	Cases []struct {
		Name   string   `json:"name"`
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"cases"`
}

func TestNormalizeSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/column-names.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := Normalize(c.Input)
			if len(got) == 0 && len(c.Output) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.Output) {
				t.Errorf("Normalize(%q) = %q, want %q", c.Input, got, c.Output)
			}
		})
	}
}

func TestNormalizeIsIdempotentAndSafe(t *testing.T) {
	in := []string{"NODE SELECTOR", "", "9lives", "NAME", "name", "\xff\xfe", "Größe"}
	once := Normalize(in)
	if twice := Normalize(once); !reflect.DeepEqual(once, twice) {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
	for _, n := range once {
		if n == "" || strings.Trim(n, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" || (n[0] >= '0' && n[0] <= '9') {
			t.Errorf("unsafe name %q", n)
		}
	}
}
