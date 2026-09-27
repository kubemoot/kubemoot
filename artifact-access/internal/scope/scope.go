// Package scope builds the artifact-reference subject the materializer follows.
// Producers publish references on kubemoot.artifacts.<ns>.<crew>.<thread>, so a
// sandbox follows only its own namespace and never materializes another
// namespace's artifacts, even for a crew of the same name.
//
// IMPORTANT: namespace resolution and token validation are shared with
// kubemoot/operator/internal/crewscope, kubemoot/discussion-gateway/internal/crewscope
// and kubemoot/scheduling-mcp/internal/scope (per-module builds cannot share
// source). If you change one, change the others.
package scope

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// NamespaceEnv is the environment variable the operator sets from the downward API.
	NamespaceEnv = "KUBEMOOT_NAMESPACE"
	// SubjectEnv overrides the reference subject the materializer subscribes to.
	SubjectEnv = "ARTIFACT_SUBJECT"
	// ServiceAccountNamespaceFile is the fallback source of the pod's namespace.
	ServiceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

	subjectArtifacts = "kubemoot.artifacts"
)

// ErrNoNamespace means neither KUBEMOOT_NAMESPACE nor the service-account file
// named a namespace.
var ErrNoNamespace = errors.New("namespace unknown: set " + NamespaceEnv +
	" or mount the service-account namespace file")

// ReferenceFilter is kubemoot.artifacts.<ns>.>, every artifact reference of
// every crew in the namespace.
func ReferenceFilter(namespace string) string {
	return fmt.Sprintf("%s.%s.>", subjectArtifacts, namespace)
}

// ResolveNamespace returns KUBEMOOT_NAMESPACE, falling back to the
// service-account namespace file. It fails when neither names a namespace or
// the value is not a single NATS subject token.
func ResolveNamespace(getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	ns := strings.TrimSpace(getenv(NamespaceEnv))
	if ns == "" {
		data, err := readFile(ServiceAccountNamespaceFile)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrNoNamespace, err)
		}
		ns = strings.TrimSpace(string(data))
	}
	if ns == "" {
		return "", ErrNoNamespace
	}
	if strings.ContainsAny(ns, ".*> \t\r\n") {
		return "", fmt.Errorf("namespace %q is not a single NATS subject token", ns)
	}
	return ns, nil
}

// MaterializerSubject is ARTIFACT_SUBJECT when set, otherwise the reference
// filter of the pod's namespace.
func MaterializerSubject(getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	if s := strings.TrimSpace(getenv(SubjectEnv)); s != "" {
		return s, nil
	}
	ns, err := ResolveNamespace(getenv, readFile)
	if err != nil {
		return "", err
	}
	return ReferenceFilter(ns), nil
}

// MaterializerSubjectFromEnvironment resolves the subject from the process
// environment and the real service-account file.
func MaterializerSubjectFromEnvironment() (string, error) {
	return MaterializerSubject(os.Getenv, os.ReadFile)
}
