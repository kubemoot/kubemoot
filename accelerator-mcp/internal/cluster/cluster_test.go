package cluster

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func check(t *testing.T, what string, ok bool, got any) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: %+v", what, got)
	}
}

func provider(ns, name string) *aiv1alpha1.ModelProvider {
	return &aiv1alpha1.ModelProvider{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func newCluster(t *testing.T, objects ...client.Object) Cluster {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return Cluster{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
}

func TestProvider_findsByName_andRefusesNoneOrTwo(t *testing.T) {
	c := newCluster(t, provider("kubemoot", "gpu-a"), provider("kubemoot", "gpu-b"), provider("other", "gpu-b"))
	p, err := c.Provider(context.Background(), "gpu-a")
	check(t, "found", err == nil && p.Name == "gpu-a", err)
	_, err = c.Provider(context.Background(), "gpu-z")
	check(t, "none", err != nil, err)
	_, err = c.Provider(context.Background(), "gpu-b")
	check(t, "two", err != nil, err)
}

func TestBoundModels_distinctSorted_forThatProviderOnly(t *testing.T) {
	model := func(ns, name, prov, m string) client.Object {
		return &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: aiv1alpha1.ModelSpec{ProviderRef: prov, Model: m}}
	}
	c := newCluster(t, model("a", "1", "gpu-a", "qwen3:8b"), model("b", "2", "gpu-a", "qwen3:14b"),
		model("b", "3", "gpu-a", "qwen3:8b"), model("c", "4", "gpu-b", "qwen3:32b"))
	got, err := c.BoundModels(context.Background(), "gpu-a")
	check(t, "bound", err == nil && len(got) == 2 && got[0] == "qwen3:14b" && got[1] == "qwen3:8b", got)
}

func TestMemoryAndParallel(t *testing.T) {
	p := provider("kubemoot", "cpu")
	check(t, "unknown", TotalMemoryMiB(p) == 0 && UsableMemoryMiB(p) == 0 && MaxParallel(p) == 1 && !HasAccelerator(p), p)
	p.Spec.Scheduling = &aiv1alpha1.ProviderScheduling{MemoryMiB: 18_432}
	check(t, "declared", TotalMemoryMiB(p) == 18_432 && UsableMemoryMiB(p) == 16_221 && !HasAccelerator(p), p)
	p.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{VRAMTotalMiB: 32_607, MaxParallel: 4}
	check(t, "discovered wins", TotalMemoryMiB(p) == 32_607 && HasAccelerator(p) && MaxParallel(p) == 4, p)
}

func TestResetRequested(t *testing.T) {
	p := provider("kubemoot", "gpu-a")
	check(t, "no annotation", !ResetRequested(p), p)
	p.Annotations = map[string]string{AnnotationReset: "t1"}
	check(t, "never calibrated", ResetRequested(p), p)
	p.Status.Calibration = &aiv1alpha1.ProviderCalibration{ResetToken: "t1"}
	check(t, "already honored", !ResetRequested(p), p)
}

func TestCondition_followsThePhase(t *testing.T) {
	check(t, "calibrated", condition(&aiv1alpha1.ProviderCalibration{Phase: PhaseCalibrated}).Status ==
		metav1.ConditionTrue, nil)
	c := condition(&aiv1alpha1.ProviderCalibration{})
	check(t, "empty phase", c.Status == metav1.ConditionFalse && c.Reason == "Unknown", c)
}

func TestRecordCalibration_retriesAConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	conflicts := 1
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider("kubemoot", "gpu-a")).
		WithStatusSubresource(&aiv1alpha1.ModelProvider{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object,
				opts ...client.SubResourceUpdateOption) error {
				if conflicts > 0 {
					conflicts--
					return apierrors.NewConflict(schema.GroupResource{Resource: "modelproviders"}, obj.GetName(),
						errors.New("changed meanwhile"))
				}
				return cl.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()
	cl := Cluster{Client: c}
	p, _ := cl.Provider(context.Background(), "gpu-a")
	calls := 0
	err := cl.RecordCalibration(context.Background(), p, func(cal *aiv1alpha1.ProviderCalibration,
		_ *aiv1alpha1.ModelProvider) {
		calls++
		cal.Phase = PhaseCalibrated
	})
	check(t, "retried after the conflict", err == nil && calls == 2 && conflicts == 0, err)
	got, _ := cl.Provider(context.Background(), "gpu-a")
	check(t, "written", got.Status.Calibration != nil && got.Status.Calibration.Phase == PhaseCalibrated, got.Status)
}
