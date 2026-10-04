package controller

import (
	"context"
	"os"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// replicateSecret copies a Secret from the operator's namespace into the target namespace
// if it doesn't already exist. Used to ensure image pull secrets are available in crew namespaces.
func replicateSecret(ctx context.Context, c client.Client, secretName, targetNamespace string) {
	replicateSecretFrom(ctx, c, secretName, operatorNamespace(), targetNamespace)
}

// secretExists reports whether a Secret with the given name is present in the namespace.
func secretExists(ctx context.Context, c client.Client, name, namespace string) bool {
	return c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &corev1.Secret{}) == nil
}

// replicateSecretFrom copies a Secret from an allowed source namespace into the target namespace
// if it doesn't already exist. Allowed sources are the operator's namespace plus the namespaces
// listed in secretSourceNamespacesEnv; every other source is refused, so a tenant cannot pull
// another namespace's credentials into its own.
func replicateSecretFrom(ctx context.Context, c client.Client, secretName, sourceNamespace, targetNamespace string) {
	log := logf.FromContext(ctx)

	if !secretSourceAllowed(sourceNamespace) {
		log.V(1).Info("Refusing to replicate secret from a namespace that is not an allowed source",
			"secret", secretName, "sourceNamespace", sourceNamespace, "targetNamespace", targetNamespace)
		return
	}

	// Check if it already exists in the target namespace
	existing := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: targetNamespace}, existing); err == nil {
		return // Already exists
	}

	// Find the source secret
	source := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: sourceNamespace}, source); err != nil {
		log.V(1).Info("Source secret not found, skipping replication", "secret", secretName, "sourceNamespace", sourceNamespace)
		return
	}

	replica := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: targetNamespace,
			Labels: map[string]string{
				labelManagedBy:                managedByValue,
				"kubemoot.ai/replicated-from": sourceNamespace,
			},
		},
		Type: source.Type,
		Data: source.Data,
	}
	if err := c.Create(ctx, replica); err != nil && !errors.IsAlreadyExists(err) {
		log.Error(err, "Failed to replicate secret", "secret", secretName, "namespace", targetNamespace)
	} else if err == nil {
		log.Info("Replicated secret", "secret", secretName, "from", sourceNamespace, "to", targetNamespace)
	}
}

// SecretRef identifies a Secret by name and optional source namespace.
// An empty Namespace means "use the operator's namespace."
type SecretRef struct {
	Name      string
	Namespace string
}

// parseReplicateSecretsAnnotation reads a comma-separated list of secret refs
// from the kubemoot.ai/replicate-secrets annotation. Each entry is either
// "<secret-name>" (sourced from the operator namespace) or "<source-ns>/<secret-name>"
// (sourced from a specific namespace). Whitespace around the comma and slash is trimmed.
// Malformed entries (empty name, "/foo", "foo/", more than one "/") are skipped.
func parseReplicateSecretsAnnotation(annotations map[string]string) []SecretRef {
	val, ok := annotations["kubemoot.ai/replicate-secrets"]
	if !ok || val == "" {
		return nil
	}
	var refs []SecretRef
	for _, entry := range strings.Split(val, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "/")
		switch len(parts) {
		case 1:
			name := strings.TrimSpace(parts[0])
			if name != "" {
				refs = append(refs, SecretRef{Name: name})
			}
		case 2:
			ns := strings.TrimSpace(parts[0])
			name := strings.TrimSpace(parts[1])
			if ns != "" && name != "" {
				refs = append(refs, SecretRef{Name: name, Namespace: ns})
			}
		}
	}
	return refs
}

// secretSourceNamespacesEnv holds a comma-separated allowlist of extra namespaces
// (beyond the operator's own) that Secrets may be replicated from. The chart sets it
// from secretReplication.allowedSourceNamespaces.
const secretSourceNamespacesEnv = "SECRET_SOURCE_NAMESPACES"

// allowedSecretSourceNamespaces returns the namespaces Secrets may be replicated from:
// the operator's namespace first, then the configured allowlist.
func allowedSecretSourceNamespaces() []string {
	allowed := []string{operatorNamespace()}
	for _, ns := range strings.Split(os.Getenv(secretSourceNamespacesEnv), ",") {
		if ns = strings.TrimSpace(ns); ns != "" && !slices.Contains(allowed, ns) {
			allowed = append(allowed, ns)
		}
	}
	return allowed
}

// secretSourceAllowed reports whether Secrets may be replicated out of the namespace.
func secretSourceAllowed(namespace string) bool {
	for _, ns := range allowedSecretSourceNamespaces() {
		if ns == namespace {
			return true
		}
	}
	return false
}

func operatorNamespace() string {
	ns := os.Getenv("OPERATOR_NAMESPACE")
	if ns == "" {
		ns = defaultOperatorNamespace
	}
	return ns
}
