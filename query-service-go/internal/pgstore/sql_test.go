package pgstore

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSearchSQLWithoutAThreshold(t *testing.T) {
	sql, args := SearchSQL("data_docs", []float64{0.5, -1}, 5, nil)
	want := `SELECT content, metadata, 1 - (embedding <=> $1::vector) AS score FROM "data_docs" ` +
		`ORDER BY embedding <=> $1::vector LIMIT $2`
	if sql != want {
		t.Errorf("sql\n got %s\nwant %s", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"[0.5,-1]", int64(5)}) {
		t.Errorf("args %#v", args)
	}
}

func TestSearchSQLWithAThreshold(t *testing.T) {
	th := 0.25
	sql, args := SearchSQL("data_docs", []float64{1}, 3, &th)
	want := `SELECT content, metadata, 1 - (embedding <=> $1::vector) AS score FROM "data_docs" ` +
		`WHERE 1 - (embedding <=> $1::vector) >= $2 ORDER BY embedding <=> $1::vector LIMIT $3`
	if sql != want {
		t.Errorf("sql\n got %s\nwant %s", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"[1]", 0.25, int64(3)}) {
		t.Errorf("args %#v", args)
	}
}

func TestTableNamesAreQuoted(t *testing.T) {
	cases := map[string]string{
		"data_plain":            `"data_plain"`,
		"data_with-hyphen":      `"data_with-hyphen"`,
		`data_x"; DROP TABLE y`: `"data_x""; DROP TABLE y"`,
	}
	for in, want := range cases {
		if got := CountSQL(in); got != "SELECT COUNT(*) FROM "+want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := VectorLiteral([]float64{0.1, 1e-7, 3, -0.25}); got != "[0.1,1e-07,3,-0.25]" {
		t.Errorf("got %s", got)
	}
	if got := VectorLiteral(nil); got != "[]" {
		t.Errorf("empty: %s", got)
	}
}

func TestClassifyMapsOnlyAMissingTable(t *testing.T) {
	missing := fmt.Errorf("query: %w", &pgconn.PgError{Code: "42P01", Message: `relation "data_x" does not exist`})
	if !errors.Is(classify(missing), ErrNoTable) {
		t.Error("42P01 must be ErrNoTable")
	}
	other := &pgconn.PgError{Code: "42703", Message: "column does not exist"}
	if errors.Is(classify(other), ErrNoTable) {
		t.Error("another SQLSTATE must not be ErrNoTable")
	}
	if classify(nil) != nil {
		t.Error("nil stays nil")
	}
	plain := errors.New("connection refused")
	if classify(plain) != plain {
		t.Error("a plain error passes through")
	}
}

func TestDecodeMetadata(t *testing.T) {
	cases := []struct {
		in      string
		want    map[string]any
		wantErr bool
	}{
		{``, map[string]any{}, false},
		{`null`, map[string]any{}, false},
		{`{}`, map[string]any{}, false},
		{`{"a":"b"}`, map[string]any{"a": "b"}, false},
		{`["x"]`, nil, true},
		{`"text"`, nil, true},
	}
	for _, c := range cases {
		got, err := decodeMetadata([]byte(c.in))
		if (err != nil) != c.wantErr || (!c.wantErr && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%q: got %v, %v", c.in, got, err)
		}
	}
}

func TestDecodeMetadataKeepsBigNumbersExact(t *testing.T) {
	got, err := decodeMetadata([]byte(`{"id":12345678901234567890}`))
	if err != nil || fmt.Sprint(got["id"]) != "12345678901234567890" {
		t.Fatalf("got %v, %v", got, err)
	}
}
