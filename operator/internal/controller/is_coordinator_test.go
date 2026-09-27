package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// A coordinator is recognized by discussRole or by the older role label, so the
// Crew controller, the resume sync, and scheduling agree on which agent it is.
func TestIsCoordinator(t *testing.T) {
	agent := func(discussRole string, labels map[string]string) *kubemootv1alpha1.Agent {
		return &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: kubemootv1alpha1.AgentSpec{DiscussRole: discussRole}}
	}
	cases := []struct {
		name string
		a    *kubemootv1alpha1.Agent
		want bool
	}{
		{"discussRole", agent(roleCoordinator, nil), true},
		{"role label, as older crews declare it", agent("", map[string]string{annoRole: roleCoordinator}), true},
		{"both", agent(roleCoordinator, map[string]string{annoRole: roleCoordinator}), true},
		{roleToolerResume, agent(roleToolerResume, map[string]string{annoRole: roleToolerResume}), false},
		{"nothing declared", agent("", nil), false},
	}
	for _, c := range cases {
		if got := isCoordinator(c.a); got != c.want {
			t.Errorf("%s: isCoordinator = %v, want %v", c.name, got, c.want)
		}
		if got := shouldBinPack(c.a); got == c.want {
			t.Errorf("%s: shouldBinPack = %v, want %v", c.name, got, !c.want)
		}
	}
}
