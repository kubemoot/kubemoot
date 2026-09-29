package controller

import (
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func cand(name string, vram int32, score int64) scheduleCandidate {
	return scheduleCandidate{
		model: &kubemootv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: kubemootv1alpha1.ModelSpec{VRAMMib: vram}},
		score: score,
	}
}

func order(cs ...scheduleCandidate) []string {
	sort.SliceStable(cs, func(i, j int) bool { return candidateBefore(cs[i], cs[j]) })
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.model.Name)
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// A tooler at bias 0.5 scores the small and the large Model equally; the small one
// must win whatever the names are, so the binding does not depend on spelling.
func TestTieGoesToTheSmallerModel(t *testing.T) {
	eq(t, order(cand("gemma4-31b", 20480, 50), cand("qwen3.5-9b", 6656, 50)), "qwen3.5-9b", "gemma4-31b")
	eq(t, order(cand("qwen3.5-9b", 6656, 50), cand("qwen3.8-27b", 18432, 50)), "qwen3.5-9b", "qwen3.8-27b")
}

func TestScoreStillDecidesFirst(t *testing.T) {
	eq(t, order(cand("small", 5000, 30), cand("large", 20000, 70)), "large", "small")
}

func TestUndeclaredFootprintSortsAfterDeclared(t *testing.T) {
	eq(t, order(cand("a-unknown", 0, 50), cand("z-known", 9000, 50)), "z-known", "a-unknown")
}

func TestFullTieFallsBackToName(t *testing.T) {
	eq(t, order(cand("b", 5000, 50), cand("a", 5000, 50)), "a", "b")
	eq(t, order(cand("b", 0, 50), cand("a", 0, 50)), "a", "b")
}
