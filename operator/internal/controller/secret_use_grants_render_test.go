package controller

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const (
	grantsChartRel   = "../../chart/kubemoot-operator"
	grantsTemplate   = "templates/secret-use-grants.yaml"
	grantsReleaseNS  = "use-grants"
	grantsReleaseSA  = "kubemoot-operator"
	grantsRelease    = "kubemoot-operator"
	grantsMCPServers = "mcpservers"
	grantsAgentKind  = "agent"
	grantsMCPKind    = "mcpserver"
	grantsHelmFailed = "helm %v: %w\n%s"
)

// renderUseGrants renders the chart's secret use grant objects with helm, exactly as an install
// would, so these tests exercise the template and not a copy of it. helm is a build requirement of
// the operator tests (CI installs it).
func renderUseGrants(extraArgs ...string) ([]*unstructured.Unstructured, error) {
	chart := filepath.Clean(grantsChartRel)
	if out, err := exec.Command("helm", "dependency", "build", chart).CombinedOutput(); err != nil {
		return nil, fmt.Errorf(grantsHelmFailed, "dependency build", err, out)
	}
	args := append([]string{"template", grantsRelease, chart, "--namespace", grantsReleaseNS,
		"--set", "security.secretUseGrants.enabled=true", "--set", "serviceAccount.name=" + grantsReleaseSA,
		"--show-only", grantsTemplate}, extraArgs...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf(grantsHelmFailed, "template", err, out)
	}
	return parseRendered(string(out))
}

func parseRendered(text string) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	for _, doc := range strings.Split(text, "\n---\n") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			return nil, err
		}
		if len(m) > 0 {
			objs = append(objs, &unstructured.Unstructured{Object: m})
		}
	}
	return objs, nil
}

// policyNamed returns the rendered ValidatingAdmissionPolicy for resource suffix (mcpserver, agent).
func policyNamed(objs []*unstructured.Unstructured, suffix string) *unstructured.Unstructured {
	for _, o := range objs {
		if o.GetKind() == "ValidatingAdmissionPolicy" && o.GetName() == "kubemoot-secret-use-"+suffix {
			return o
		}
	}
	return nil
}
