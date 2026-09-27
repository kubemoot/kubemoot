// Package scope identifies the namespace and crew this MCP server serves.
// Scheduled records carry both, so the same crew name in two namespaces never
// sees or cancels the other's schedules.
//
// IMPORTANT: namespace resolution and token validation are shared with
// kubemoot/operator/internal/crewscope, kubemoot/discussion-gateway/internal/crewscope
// and kubemoot/artifact-access/internal/scope (per-module builds cannot share
// source). If you change one, change the others.
package scope

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// NamespaceEnv is the environment variable the operator sets from the downward API.
const NamespaceEnv = "KUBEMOOT_NAMESPACE"

// ServiceAccountNamespaceFile is the fallback source of the pod's namespace.
const ServiceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// ErrNoNamespace means neither KUBEMOOT_NAMESPACE nor the service-account file
// named a namespace.
var ErrNoNamespace = errors.New("namespace unknown: set " + NamespaceEnv +
	" or mount the service-account namespace file")

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

// NamespaceFromEnvironment resolves the namespace from the process environment
// and the real service-account file.
func NamespaceFromEnvironment() (string, error) {
	return ResolveNamespace(os.Getenv, os.ReadFile)
}
