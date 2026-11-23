package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodRegistry = `{"sources":[
 {"name":"docs","namespace":"pilot","vectorStore":{"type":"pgvector","endpoint":"pg.db:5432/vectors","collection":"docs-v2","dimensions":768},
  "embedding":{"endpoint":"http://embed:8000","model":"nomic"},"defaultTopK":7},
 {"name":"resumes","namespace":"pilot","vectorStore":{"endpoint":"pg.db:5432","collection":"resumes","dimensions":768},
  "embedding":{"endpoint":"http://embed:8000","model":"nomic"}}]}`

func TestParseAcceptsAGoodFile(t *testing.T) {
	f, err := Parse([]byte(goodRegistry))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sources) != 2 || f.Sources[1].VectorStore.Type != "pgvector" {
		t.Fatalf("got %+v", f.Sources)
	}
	if got := f.Sources[0].Table(); got != "data_docs-v2" {
		t.Fatalf("table %q", got)
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	src := func(extra string) string {
		return `{"sources":[{"name":"a","vectorStore":{"endpoint":"h:1","collection":"c"},"embedding":{"endpoint":"e","model":"m"}` + extra + `}]}`
	}
	cases := map[string]string{
		"not json":          `{`,
		"unknown field":     src(`,"colection":"x"`),
		"bad name":          strings.Replace(src(""), `"name":"a"`, `"name":"A_b"`, 1),
		"name with slash":   strings.Replace(src(""), `"name":"a"`, `"name":"a/b"`, 1),
		"no collection":     strings.Replace(src(""), `"collection":"c"`, `"collection":""`, 1),
		"no embedding":      strings.Replace(src(""), `"model":"m"`, `"model":""`, 1),
		"top k too big":     src(`,"defaultTopK":101`),
		"negative top k":    src(`,"defaultTopK":-1`),
		"other store":       strings.Replace(src(""), `"endpoint":"h:1"`, `"type":"qdrant","endpoint":"h:1"`, 1),
		"duplicate sources": `{"sources":[` + dupBody() + `]}`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted %s", name, doc)
		}
	}
}

func dupBody() string {
	one := `{"name":"a","vectorStore":{"endpoint":"h:1","collection":"c"},"embedding":{"endpoint":"e","model":"m"}}`
	return one + "," + one
}

func TestParseAcceptsAnEmptyRegistry(t *testing.T) {
	f, err := Parse([]byte(`{"sources":[]}`))
	if err != nil || len(f.Sources) != 0 {
		t.Fatalf("got %v, %v", f, err)
	}
}

func TestTopKFallsBackToTheServiceDefault(t *testing.T) {
	f, _ := Parse([]byte(goodRegistry))
	if got := f.Sources[0].TopK(5); got != 7 {
		t.Errorf("source default: %d", got)
	}
	if got := f.Sources[1].TopK(5); got != 5 {
		t.Errorf("service default: %d", got)
	}
}

func TestBuildResolvesEntriesInFileOrder(t *testing.T) {
	snap, err := Build([]byte(goodRegistry), Env{User: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(snap.Names(), ","); got != "docs,resumes" {
		t.Fatalf("names %s", got)
	}
	e, ok := snap.Lookup("docs")
	if !ok || e.Conn.Host != "pg.db" || e.Conn.User != "u" || e.Conn.DBName != defaultDBName {
		t.Fatalf("entry %+v", e)
	}
	if _, ok := snap.Lookup("missing"); ok {
		t.Fatal("found a source that is not listed")
	}
}

func TestFingerprintChangesWithCredentialsButNotVersion(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "password"), "one\n")
	doc := strings.Replace(goodRegistry, `"collection":"resumes"`, `"collection":"resumes","credentialsDir":"`+dir+`"`, 1)
	a, err := Build([]byte(doc), Env{})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "password"), "two\n")
	b, err := Build([]byte(doc), Env{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != b.Version {
		t.Error("the file did not change, its version should not")
	}
	if a.Fingerprint == b.Fingerprint {
		t.Error("a rotated password must change the fingerprint")
	}
}

func TestBuildFailsWhenCredentialsAreMissing(t *testing.T) {
	doc := strings.Replace(goodRegistry, `"collection":"resumes"`, `"collection":"resumes","credentialsDir":"/nonexistent"`, 1)
	if _, err := Build([]byte(doc), Env{}); err == nil {
		t.Fatal("expected an error")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
