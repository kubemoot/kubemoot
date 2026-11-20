package controller

import "testing"

func TestHeldPickPreferFasterIsBoundedToInt32(t *testing.T) {
	cases := map[string]int32{
		"50":           50,
		"2147483647":   2147483647,
		"2147483648":   0, // beyond int32: not held
		"99999999999":  0,
		"-5":           0,
		"not-a-number": 0,
		"":             0,
	}
	for in, want := range cases {
		p := heldPick("m", "http://e", "", "", in)
		if p == nil {
			t.Fatalf("preferFaster %q: pick is nil", in)
		}
		if p.PreferFaster != want {
			t.Errorf("preferFaster %q: got %d, want %d", in, p.PreferFaster, want)
		}
	}
}
