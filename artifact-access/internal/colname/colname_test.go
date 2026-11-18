package colname

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
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

// listFixture is the cases shared with agent-runtime's ColumnNamesTest.
type listFixture struct {
	Cases []struct {
		Name     string   `json:"name"`
		Input    []string `json:"input"`
		Generate int      `json:"generate"`
		Max      *int     `json:"max"`
		Output   string   `json:"output"`
	} `json:"cases"`
}

func TestListSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/column-list.json")
	if err != nil {
		t.Fatal(err)
	}
	var f listFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			names := c.Input
			for i := 1; i <= c.Generate; i++ {
				names = append(names, "c"+strconv.Itoa(i))
			}
			got := List(names)
			if c.Max != nil {
				got = ListMax(names, *c.Max)
			}
			if got != c.Output {
				t.Errorf("list of %d names = %q, want %q", len(names), got, c.Output)
			}
		})
	}
}

func TestListMaxIgnoresANegativeLimit(t *testing.T) {
	if got := ListMax([]string{"a", "b"}, -1); got != "+2more" {
		t.Errorf("got %q", got)
	}
}

// nameCol is a column name the tests use more than once.
const nameCol = "name"

func TestClosest(t *testing.T) {
	cols := []string{"namespace", nameCol, "status", "restarts", "_call"}
	tests := []struct {
		guess string
		want  []string
	}{
		{"status.phase", []string{"status"}},
		{"involved_object_name", []string{nameCol}},
		{"InvolvedObject.Name", []string{nameCol}},
		{"allocatable_memory", nil},
		{"id", nil},
		{"", nil},
	}
	for _, tc := range tests {
		if got := Closest(tc.guess, cols); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Closest(%q) = %q, want %q", tc.guess, got, tc.want)
		}
	}
	many := []string{"a_name", "b_name", "c_name", "d_name"}
	if got := Closest(nameCol, many); len(got) != maxClosest {
		t.Errorf("at most %d names are offered, got %q", maxClosest, got)
	}
}
