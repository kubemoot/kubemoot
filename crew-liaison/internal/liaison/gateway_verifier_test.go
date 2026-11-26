package liaison

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	gwNS   = "ns"
	gwCrew = "guide"
	gwSA   = "guide-discussion"
	crewID = types.UID("crew-uid-1")
)

func ownedBy(crew string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: kubemootv1alpha1.GroupVersion.String(), Kind: "Crew", Name: crew, UID: uid, Controller: ptr.To(true),
	}}
}

func gatewayCluster(t *testing.T, objs ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kubemootv1alpha1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func crewObject() *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Namespace: gwNS, Name: gwCrew, UID: crewID}}
}

func serviceAccount(owners []metav1.OwnerReference) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: gwNS, Name: gwSA, OwnerReferences: owners}}
}

func TestOnlyTheServiceAccountTheOperatorMadeForTheCrewIsItsGateway(t *testing.T) {
	cases := map[string]struct {
		objs []client.Object
		want bool
	}{
		"made by the operator for the crew": {[]client.Object{crewObject(), serviceAccount(ownedBy(gwCrew, crewID))}, true},
		"created by hand, no owner":         {[]client.Object{crewObject(), serviceAccount(nil)}, false},
		"owned by another crew":             {[]client.Object{crewObject(), serviceAccount(ownedBy("other", crewID))}, false},
		"owned by a crew that was replaced": {[]client.Object{crewObject(), serviceAccount(ownedBy(gwCrew, "old-uid"))}, false},
		"the crew does not exist":           {[]client.Object{serviceAccount(ownedBy(gwCrew, crewID))}, false},
		"the ServiceAccount does not exist": {[]client.Object{crewObject()}, false},
		"owner is not a controller kind": {[]client.Object{crewObject(), serviceAccount([]metav1.OwnerReference{{
			APIVersion: "v1", Kind: "ConfigMap", Name: gwCrew, UID: crewID, Controller: ptr.To(true)}})}, false},
		"owner is not the controller": {[]client.Object{crewObject(), serviceAccount([]metav1.OwnerReference{{
			APIVersion: kubemootv1alpha1.GroupVersion.String(), Kind: "Crew", Name: gwCrew, UID: crewID}})}, false},
	}
	for name, c := range cases {
		got, err := NewGatewayVerifier(gatewayCluster(t, c.objs...)).Verify(context.Background(), gwNS, gwSA, gwCrew)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", name, got, err, c.want)
		}
	}
}

func TestAReadFailureIsAnErrorNotANo(t *testing.T) {
	if _, err := NewGatewayVerifier(failingReader{}).Verify(context.Background(), gwNS, gwSA, gwCrew); err == nil {
		t.Fatal("a failing read must be an error")
	}
	// A failing read of the Crew, after the ServiceAccount was found.
	r := gatewayCluster(t, serviceAccount(ownedBy(gwCrew, crewID)))
	if _, err := NewGatewayVerifier(crewReadFails{r}).Verify(context.Background(), gwNS, gwSA, gwCrew); err == nil {
		t.Fatal("a failing Crew read must be an error")
	}
}

type crewReadFails struct{ client.Reader }

func (c crewReadFails) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isCrew := obj.(*kubemootv1alpha1.Crew); isCrew {
		return errors.New("api away")
	}
	return c.Reader.Get(ctx, key, obj, opts...)
}
