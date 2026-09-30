package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestOverlaySecurityContext_OverridesOnlyWhatIsSet(t *testing.T) {
	base := &corev1.Container{}
	applyMCPServerSecurityContext(base, false)
	readOnly, escalate := true, true
	got := overlaySecurityContext(base.SecurityContext, &corev1.SecurityContext{ReadOnlyRootFilesystem: &readOnly, AllowPrivilegeEscalation: &escalate})

	if got.ReadOnlyRootFilesystem == nil || !*got.ReadOnlyRootFilesystem {
		t.Error("expected readOnlyRootFilesystem from the override")
	}
	if got.AllowPrivilegeEscalation == nil || !*got.AllowPrivilegeEscalation {
		t.Error("expected the override to replace allowPrivilegeEscalation")
	}
	if !keepsModeHardening(got) {
		t.Errorf("expected the mode's drop-all capabilities and seccomp profile kept, got %+v", got)
	}
	if base.SecurityContext.ReadOnlyRootFilesystem != nil {
		t.Error("the base must not be modified")
	}
}

// keepsModeHardening reports whether sc still drops every capability and uses
// the RuntimeDefault seccomp profile, as both security modes set.
func keepsModeHardening(sc *corev1.SecurityContext) bool {
	dropsAll := sc.Capabilities != nil && len(sc.Capabilities.Drop) == 1 && sc.Capabilities.Drop[0] == corev1.Capability(capabilityAll)
	return dropsAll && sc.SeccompProfile != nil && sc.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault
}

const capabilityAll = "ALL"

func TestOverlaySecurityContext_NilOverrideKeepsBase(t *testing.T) {
	base := &corev1.SecurityContext{}
	if overlaySecurityContext(base, nil) != base {
		t.Error("expected the base unchanged without an override")
	}
	readOnly := true
	got := overlaySecurityContext(nil, &corev1.SecurityContext{ReadOnlyRootFilesystem: &readOnly})
	if got == nil || got.ReadOnlyRootFilesystem == nil || !*got.ReadOnlyRootFilesystem {
		t.Error("expected the override alone when there is no base")
	}
}

func TestBuildDeployment_AppliesSpecSecurityContext(t *testing.T) {
	readOnly := true
	mcp := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "readops", Namespace: "ns"},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:           "artifact-access:1",
			Transport:       kubemootv1alpha1.TransportHTTP,
			SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: &readOnly},
		},
	}
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	sc := r.buildDeployment(mcp).Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatalf("expected readOnlyRootFilesystem on the MCP server container, got %+v", sc)
	}
	if !keepsModeHardening(sc) {
		t.Error("expected the default hardening kept")
	}
}
