// Package crewscope builds the NATS subjects the discussion gateway uses for one
// crew in one namespace. Every subject carries the namespace first, then the crew,
// so the same crew name can run in many namespaces without crosstalk.
//
// IMPORTANT: the subject formats, token validation and namespace resolution are
// shared with kubemoot/operator/internal/crewscope, kubemoot/scheduling-mcp/internal/scope
// and kubemoot/artifact-access/internal/scope (per-module builds cannot share
// source). If you change one, change the others.
package crewscope

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

const (
	subjectRequest = "kubemoot.request"
	subjectDiscuss = "kubemoot.discuss"
)

// ErrNoNamespace means neither KUBEMOOT_NAMESPACE nor the service-account file
// named a namespace.
var ErrNoNamespace = errors.New("namespace unknown: set " + NamespaceEnv +
	" or mount the service-account namespace file")

// Scope is one crew in one namespace.
type Scope struct {
	Namespace string
	Crew      string
}

// New validates both names as single NATS tokens and returns the scope.
func New(namespace, crew string) (Scope, error) {
	if err := validToken("namespace", namespace); err != nil {
		return Scope{}, err
	}
	if err := validToken("crew", crew); err != nil {
		return Scope{}, err
	}
	return Scope{Namespace: namespace, Crew: crew}, nil
}

// RequestSubject is kubemoot.request.<ns>.<crew>.
func (s Scope) RequestSubject() string {
	return fmt.Sprintf("%s.%s.%s", subjectRequest, s.Namespace, s.Crew)
}

// DiscussSubject is kubemoot.discuss.<ns>.<crew>.<channel>.<thread>.
func (s Scope) DiscussSubject(channel, thread string) string {
	return fmt.Sprintf("%s.%s.%s.%s.%s", subjectDiscuss, s.Namespace, s.Crew, channel, thread)
}

// DiscussFilter is kubemoot.discuss.<ns>.<crew>.>, every discussion message of the crew.
func (s Scope) DiscussFilter() string {
	return fmt.Sprintf("%s.%s.%s.>", subjectDiscuss, s.Namespace, s.Crew)
}

// ResolveNamespace returns KUBEMOOT_NAMESPACE, falling back to the
// service-account namespace file. It fails when neither names a namespace.
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
	if err := validToken("namespace", ns); err != nil {
		return "", err
	}
	return ns, nil
}

// NamespaceFromEnvironment resolves the namespace from the process environment
// and the real service-account file.
func NamespaceFromEnvironment() (string, error) {
	return ResolveNamespace(os.Getenv, os.ReadFile)
}

// validToken rejects a value that is empty or would not be exactly one NATS token.
func validToken(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", what)
	}
	if strings.ContainsAny(v, ".*> \t\r\n") {
		return fmt.Errorf("%s %q is not a single NATS subject token", what, v)
	}
	return nil
}
