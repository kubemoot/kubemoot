package controller

import (
	"bytes"
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
// and keeps the copy in sync with the source. Used to ensure image pull secrets are available in crew namespaces.
func replicateSecret(ctx context.Context, c client.Client, secretName, targetNamespace string) {
	replicateSecretFrom(ctx, c, secretName, operatorNamespace(), targetNamespace)
}

// secretExists reports whether a Secret with the given name is present in the namespace.
func secretExists(ctx context.Context, c client.Client, name, namespace string) bool {
	return c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &corev1.Secret{}) == nil
}

// labelReplicatedFrom records the namespace a replica was copied from. Together with
// labelManagedBy it marks a Secret as an operator-made copy that may be kept in sync.
const labelReplicatedFrom = "kubemoot.ai/replicated-from"

// replicateSecretFrom copies a Secret from an allowed source namespace into the target namespace
// and keeps an operator-made copy in step with its source. Allowed sources are the operator's
// namespace plus the namespaces listed in secretSourceNamespacesEnv; every other source is
// refused, so a tenant cannot pull another namespace's credentials into its own.
//
// A same-named Secret the operator did not create is never modified. Deleting the source
// leaves existing copies in place: they keep working until the source returns, and removing
// credentials a running pod still mounts would break image pulls for no gain.
func replicateSecretFrom(ctx context.Context, c client.Client, secretName, sourceNamespace, targetNamespace string) {
	log := logf.FromContext(ctx)

	if !secretSourceAllowed(sourceNamespace) {
		log.V(1).Info("Refusing to replicate secret from a namespace that is not an allowed source",
			"secret", secretName, "sourceNamespace", sourceNamespace, "targetNamespace", targetNamespace)
		return
	}

	source := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: sourceNamespace}, source); err != nil {
		log.V(1).Info("Source secret not found, skipping replication", "secret", secretName, "sourceNamespace", sourceNamespace)
		return
	}

	existing := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: targetNamespace}, existing)
	switch {
	case err == nil:
		syncReplica(ctx, c, source, existing)
	case errors.IsNotFound(err):
		createReplica(ctx, c, source, targetNamespace)
	default:
		log.Error(err, "Failed to read replica target", "secret", secretName, "namespace", targetNamespace)
	}
}

func createReplica(ctx context.Context, c client.Client, source *corev1.Secret, targetNamespace string) {
	log := logf.FromContext(ctx)
	replica := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Name,
			Namespace: targetNamespace,
			Labels: map[string]string{
				labelManagedBy:      managedByValue,
				labelReplicatedFrom: source.Namespace,
			},
		},
		Type: source.Type,
		Data: source.Data,
	}
	if err := c.Create(ctx, replica); err != nil && !errors.IsAlreadyExists(err) {
		log.Error(err, "Failed to replicate secret", "secret", source.Name, "namespace", targetNamespace)
	} else if err == nil {
		log.Info("Replicated secret", "secret", source.Name, "from", source.Namespace, "to", targetNamespace)
	}
}

// isOperatorReplica reports whether the Secret is an operator-made copy of a Secret in sourceNamespace.
func isOperatorReplica(s *corev1.Secret, sourceNamespace string) bool {
	return s.Labels[labelManagedBy] == managedByValue && s.Labels[labelReplicatedFrom] == sourceNamespace
}

// syncReplica brings an operator-made copy's Data and Type in line with its source and writes
// only when they differ. A Secret the operator did not create is left untouched.
func syncReplica(ctx context.Context, c client.Client, source, existing *corev1.Secret) {
	log := logf.FromContext(ctx)
	if !isOperatorReplica(existing, source.Namespace) {
		log.V(1).Info("Secret exists but was not replicated by the operator from this source, leaving it",
			"secret", existing.Name, "namespace", existing.Namespace, "sourceNamespace", source.Namespace)
		return
	}
	if secretDataEqual(existing.Data, source.Data) {
		return
	}
	if existing.Type != source.Type {
		// Secret type is immutable on the API server; the copy keeps its type and only its data follows.
		log.V(1).Info("Replicated secret type differs from source and cannot be changed in place",
			"secret", existing.Name, "namespace", existing.Namespace, "copyType", existing.Type, "sourceType", source.Type)
	}
	updated := existing.DeepCopy()
	updated.Data = source.Data
	if err := c.Update(ctx, updated); err != nil {
		log.Error(err, "Failed to update replicated secret", "secret", existing.Name, "namespace", existing.Namespace)
		return
	}
	log.Info("Updated replicated secret from source", "secret", existing.Name, "from", source.Namespace, "to", existing.Namespace)
}

// secretDataEqual compares two Secret data maps, treating nil and empty as equal.
func secretDataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !bytes.Equal(av, bv) {
			return false
		}
	}
	return true
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
