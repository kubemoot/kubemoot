package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func collect(t *testing.T, in string, stopAt string) ([]string, error) {
	t.Helper()
	var got []string
	err := Data(strings.NewReader(in), func(d string) bool {
		got = append(got, d)
		return d != stopAt
	})
	return got, err
}

func TestDataPayloads(t *testing.T) {
	in := ": a comment\n" +
		"event: phase\n" +
		"data: {\"type\":\"a\"}\n" +
		"data:{\"type\":\"b\"}\n" + // no space after the colon
		"  data:   {\"type\":\"c\"}  \n" + // stray whitespace
		"data:\n" + // blank payload
		"data: [DONE]\n" + // sentinel
		"\n" +
		"id: 7\n" +
		"data: {\"type\":\"d\"}\n"
	got, err := collect(t, in, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`{"type":"a"}`, `{"type":"b"}`, `{"type":"c"}`, `{"type":"d"}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestDataStopsWhenAsked(t *testing.T) {
	got, err := collect(t, "data: 1\ndata: 2\ndata: 3\n", "2")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestDataEmptyAndNoData(t *testing.T) {
	for _, in := range []string{"", "\n\n", "event: x\n: c\n"} {
		got, err := collect(t, in, "")
		if err != nil || len(got) != 0 {
			t.Fatalf("%q: got %v err %v", in, got, err)
		}
	}
}

func TestDataLongLine(t *testing.T) {
	long := "data: " + strings.Repeat("x", 3*1024*1024) + "\n"
	got, err := collect(t, long, "")
	if err != nil || len(got) != 1 || len(got[0]) != 3*1024*1024 {
		t.Fatalf("3 MiB line: n=%d err=%v", len(got), err)
	}
	tooLong := "data: " + strings.Repeat("x", maxLine+1) + "\n"
	if _, err := collect(t, tooLong, ""); err == nil {
		t.Fatal("line beyond the limit should be an error")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestDataReaderError(t *testing.T) {
	err := Data(io.MultiReader(strings.NewReader("data: 1\n"), failingReader{}), func(string) bool { return true })
	if err == nil || err.Error() != "boom" {
		t.Fatalf("want reader error, got %v", err)
	}
}
