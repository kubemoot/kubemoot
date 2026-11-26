package liaison

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// k8sGateways verifies a gateway's ServiceAccount against the cluster: the Crew exists, and the
// ServiceAccount is controlled by that very Crew (the operator makes it so), so a name copied
// into a ServiceAccount someone else created does not pass.
type k8sGateways struct{ reader client.Reader }

// NewGatewayVerifier returns a verifier that reads the Crew and the ServiceAccount with reader.
func NewGatewayVerifier(reader client.Reader) GatewayVerifier { return &k8sGateways{reader: reader} }

// Verify implements GatewayVerifier.
func (g *k8sGateways) Verify(ctx context.Context, namespace, serviceAccount, crew string) (bool, error) {
	sa := &corev1.ServiceAccount{}
	if err := g.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: serviceAccount}, sa); err != nil {
		return false, notFoundIsNo(err)
	}
	owner := metav1.GetControllerOf(sa)
	if owner == nil || owner.Kind != "Crew" || owner.Name != crew ||
		owner.APIVersion != kubemootv1alpha1.GroupVersion.String() {
		return false, nil
	}
	found := &kubemootv1alpha1.Crew{}
	if err := g.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: crew}, found); err != nil {
		return false, notFoundIsNo(err)
	}
	return found.UID == owner.UID, nil
}

// notFoundIsNo turns "not there" into a plain no and any other failure into an error.
func notFoundIsNo(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("reading the cluster: %w", err)
}
