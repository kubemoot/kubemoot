package workspace

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/workspace-access/internal/colname"
)

func TestSQLiteNoSuchColumnListsTheTableColumns(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": "NAME,STATUS,NODE\npod-a,Running,n1\n"})
	_, err := s.Query(context.Background(), map[string]string{"pods": "k"}, "SELECT TAINTS FROM pods")
	if err == nil {
		t.Fatal("a column the table lacks must fail")
	}
	for _, want := range []string{"no such column", "pods (name,status,node)"} {
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
	cols := make([]string, colname.MaxListed+5)
	for i := range cols {
		cols[i] = "c" + strconv.Itoa(i)
	}
	err := withColumns(errors.New("no such column: x"), []tableColumns{{table: "t", columns: cols}})
	if !strings.Contains(err.Error(), ",+5more") || strings.Contains(err.Error(), "c64") {
		t.Errorf("wide table not bounded: %v", err)
	}
	if withColumns(nil, nil) != nil {
		t.Error("no error stays no error")
	}
}

func TestSQLiteKubectlTableColumnsNeedNoQuoting(t *testing.T) {
	table := "NAMESPACE     NAME   UP-TO-DATE   NODE SELECTOR   AGE\nkube-system   ds-a   1            disk=ssd        3d\n"
	s := sqliteService(t, map[string]string{"k": table})
	out := query(t, s, map[string]string{"d": "k"}, "SELECT d.node_selector, d.UP_TO_DATE FROM d")
	if !strings.Contains(out, "disk=ssd | 1") {
		t.Fatalf("got %q", out)
	}
	_, err := s.Query(context.Background(), map[string]string{"d": "k"}, "SELECT d.TAINTS FROM d")
	if err == nil || !strings.Contains(err.Error(), "d (namespace,name,up_to_date,node_selector,age)") {
		t.Fatalf("the error lists the normalized names: %v", err)
	}
}

func TestNoSuchColumnNamesTheClosestRealColumns(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": "NAME,STATUS,NODE\npod-a,Running,n1\n"})
	_, err := s.Query(context.Background(), map[string]string{tblPods: "k"}, "SELECT pods.status_phase FROM pods")
	if err == nil || !strings.Contains(err.Error(), "closest to pods.status_phase: pods.status") {
		t.Fatalf("the error names the close column: %v", err)
	}
	_, err = s.Query(context.Background(), map[string]string{tblPods: "k"}, "SELECT allocatable_memory FROM pods")
	if err == nil || strings.Contains(err.Error(), "closest to") {
		t.Fatalf("no close column means no suggestion: %v", err)
	}
}

func TestNoSuchColumnOnPlainTextSaysSo(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": "the pod restarted\nand again\n"})
	_, err := s.Query(context.Background(), map[string]string{"events": "k"}, "SELECT reason FROM events")
	if err == nil || !strings.Contains(err.Error(), "events (line_no,line)") ||
		!strings.Contains(err.Error(), "events read as plain text lines") {
		t.Fatalf("a text table says it is text: %v", err)
	}
}

func TestYAMLListAfterACommentLineIsATable(t *testing.T) {
	y := "# The following events (YAML format) were found:\n- reason: Failed\n  message: boom\n- reason: Killing\n  message: ok\n"
	s := sqliteService(t, map[string]string{"k": y})
	out := query(t, s, map[string]string{"e": "k"}, "SELECT reason FROM e WHERE message = 'boom'")
	if !strings.Contains(out, "Failed") {
		t.Fatalf("got %q", out)
	}
}
