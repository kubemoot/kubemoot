package controller

import (
	"context"
	"os"
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

// replicateSecretFrom copies a Secret from a specific source namespace into the target namespace
// if it doesn't already exist. Used when the source secret lives outside the operator namespace
// (e.g., DB credentials discovered from another crew's RAGSource).
func replicateSecretFrom(ctx context.Context, c client.Client, secretName, sourceNamespace, targetNamespace string) {
	log := logf.FromContext(ctx)

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
				labelManagedBy: managedByValue,
				"kubemoot.ai/replicated-from":  sourceNamespace,
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

func operatorNamespace() string {
	ns := os.Getenv("OPERATOR_NAMESPACE")
	if ns == "" {
		ns = "kubemoot"
	}
	return ns
}
