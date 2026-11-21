package signin

import (
	"strings"
	"testing"
)

func TestSealRoundTripAndRotation(t *testing.T) {
	k1, k2 := testKey(t, "k1"), testKey(t, "k2")
	old, err := ParseKeys(k1)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := old.Seal(purposeCode, map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}

	// New key seals, the old one still opens.
	rotated, err := ParseKeys(k2 + "\n" + k1)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := rotated.Open(purposeCode, blob, &got); err != nil || got["a"] != "b" {
		t.Fatalf("old blob under rotated keys: %v %v", err, got)
	}
	fresh, _ := rotated.Seal(purposeCode, map[string]string{"a": "c"})
	if fresh == blob {
		t.Error("blobs must differ")
	}
	if err := old.Open(purposeCode, fresh, &got); err == nil {
		t.Error("a key the ring lacks must not open the blob")
	}
	if err := rotated.Open(purposeState, blob, &got); err == nil {
		t.Error("a blob must not open for another purpose")
	}
}

func TestOpenRefusesGarbage(t *testing.T) {
	s, _ := ParseKeys(testKey(t, "k"))
	var v map[string]string
	for _, in := range []string{"", "!", "AA", "AQ", "AQFr", strings.Repeat("A", 200)} {
		if s.Open(purposeCode, in, &v) == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestParseKeysErrors(t *testing.T) {
	good := testKey(t, "k1")
	for name, spec := range map[string]string{
		"empty":        "",
		"no colon":     "k1",
		"bad base64":   "k1:***",
		"short key":    "k1:AAAA",
		"bad id":       "bad id:" + strings.SplitN(good, ":", 2)[1],
		"duplicate id": good + "\n" + good,
	} {
		if _, err := ParseKeys(spec); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func FuzzOpen(f *testing.F) {
	s, _ := ParseKeys("k:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	good, _ := s.Seal(purposeCode, map[string]string{"a": "b"})
	f.Add(good)
	f.Add("")
	f.Add("AQFr")
	f.Fuzz(func(t *testing.T, blob string) {
		var v map[string]string
		_ = s.Open(purposeCode, blob, &v) // must not panic
	})
}
