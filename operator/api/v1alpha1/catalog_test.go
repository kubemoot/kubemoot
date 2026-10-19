package v1alpha1

import "testing"

func TestSizeClassForBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int64
		want  string
	}{
		{"unknown zero", 0, ""},
		{"negative", -5, ""},
		{"half a billion", 500_000_000, SizeSmall},
		{"just under 12B", 11_900_000_000, SizeSmall},
		{"exactly 12B", 12_000_000_000, SizeMedium},
		{"just under 30B", 29_900_000_000, SizeMedium},
		{"exactly 30B", 30_000_000_000, SizeLarge},
		{"70B", 70_000_000_000, SizeLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SizeClassFor(tc.count); got != tc.want {
				t.Errorf("SizeClassFor(%d) = %q, want %q", tc.count, got, tc.want)
			}
		})
	}
}

func TestParseParameterCount(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"14B", 14_000_000_000, true},
		{"8.2B", 8_200_000_000, true},
		{"0.5B", 500_000_000, true},
		{"751.63M", 751_630_000, true},
		{"1T", 1_000_000_000_000, true},
		{" 7b ", 7_000_000_000, true},
		{"", 0, false},
		{"B", 0, false},
		{"14", 0, false},
		{"14X", 0, false},
		{"abcB", 0, false},
		{"-3B", 0, false},
		{"0B", 0, false},
		{"NaNB", 0, false},
		{"InfB", 0, false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseParameterCount(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("ParseParameterCount(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
