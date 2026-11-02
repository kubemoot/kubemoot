package workspace

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestSQLiteNoSuchColumnListsTheTableColumns(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": "NAME,STATUS,NODE\npod-a,Running,n1\n"})
	_, err := s.Query(context.Background(), map[string]string{"pods": "k"}, "SELECT TAINTS FROM pods")
	if err == nil {
		t.Fatal("a column the table lacks must fail")
	}
	for _, want := range []string{"no such column", "pods (NAME, STATUS, NODE)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestSQLiteOtherErrorsDoNotListColumns(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": "NAME\na\n"})
	_, err := s.Query(context.Background(), map[string]string{"pods": "k"}, "SELECT * FROM nothere")
	if err == nil || strings.Contains(err.Error(), "the columns are") {
		t.Fatalf("a missing table is not a column error: %v", err)
	}
}

func TestWithColumnsBoundsAWideTable(t *testing.T) {
	cols := make([]string, maxListedColumns+5)
	for i := range cols {
		cols[i] = "c" + strconv.Itoa(i)
	}
	err := withColumns(errors.New("no such column: x"), []tableColumns{{table: "t", columns: cols}})
	if !strings.Contains(err.Error(), "and 5 more") || strings.Contains(err.Error(), "c64") {
		t.Errorf("wide table not bounded: %v", err)
	}
	if withColumns(nil, nil) != nil {
		t.Error("no error stays no error")
	}
}
