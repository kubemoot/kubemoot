package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/mesh"
	"github.com/kubemoot/kubemoot/operator/internal/natspod"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

// setNATSOptions sets the chart-provided source the way the operator's Deployment does.
func setNATSOptions(t *testing.T) {
	t.Helper()
	t.Setenv(natspod.EnvCASecret, "nats-ca")
	t.Setenv(natspod.EnvAuthSecret, "nats-auth")
	t.Setenv(natspod.EnvAuthUserKey, "user")
	t.Setenv(natspod.EnvAuthPasswdKey, "password")
}

func containerNamed(t *testing.T, spec corev1.PodSpec, name string) corev1.Container {
	t.Helper()
	for _, list := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, c := range list {
			if c.Name == name {
				return c
			}
		}
	}
	t.Fatalf("no container %q", name)
	return corev1.Container{}
}

func wantNATSOptions(t *testing.T, what string, c corev1.Container, spec corev1.PodSpec) {
	t.Helper()
	got := map[string]bool{}
	for _, e := range c.Env {
		got[e.Name] = true
	}
	for _, name := range []string{natsconn.EnvCAFile, natsconn.EnvUser, natsconn.EnvPassword} {
		if !got[name] {
			t.Errorf("%s: %s is not set in %v", what, name, c.Env)
		}
	}
	if len(spec.Volumes) == 0 {
		t.Errorf("%s: no volumes", what)
	}
}

func wantNoNATSOptions(t *testing.T, what string, c corev1.Container) {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == natsconn.EnvCAFile || e.Name == natsconn.EnvUser {
			t.Errorf("%s: %s set without chart options", what, e.Name)
		}
	}
}

func TestAgentPodsGetTheNATSOptions(t *testing.T) {
	setNATSOptions(t)
	r, agent := replicasFixture(t, nil, roleCoordinator, 1)
	d := deploymentFor(r, agent)
	wantNATSOptions(t, "classic agent", containerNamed(t, d.Spec.Template.Spec, "agent"), d.Spec.Template.Spec)

	sd := buildSidecarPod(t, sidecarAgent())
	wantNATSOptions(t, "sidecar", containerNamed(t, sd.Spec.Template.Spec, mesh.SidecarContainerName), sd.Spec.Template.Spec)
	for _, e := range containerNamed(t, sd.Spec.Template.Spec, harnessContainerName).Env {
		if e.Name == natsconn.EnvUser || e.Name == natsconn.EnvPassword {
			t.Errorf("the harness must never see the NATS credential: %s", e.Name)
		}
	}
}

func TestAgentPodsGetNothingWithoutChartOptions(t *testing.T) {
	r, agent := replicasFixture(t, nil, roleCoordinator, 1)
	d := deploymentFor(r, agent)
	wantNoNATSOptions(t, "classic agent", containerNamed(t, d.Spec.Template.Spec, "agent"))
}

func TestCrewGatewayGetsTheNATSOptions(t *testing.T) {
	setNATSOptions(t)
	r := &CrewReconciler{ConfigCache: NewConfigCache()}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: testCrewNamespace}}
	d := r.buildDeployment(crew)
	wantNATSOptions(t, "gateway", containerNamed(t, d.Spec.Template.Spec, "discussion-gateway"), d.Spec.Template.Spec)
}

func TestFitnessRunnerGetsTheNATSOptionsOnlyWhenItDialsNATS(t *testing.T) {
	setNATSOptions(t)
	t.Setenv(envNATSURL, "tls://nats:4222")
	r := &CrewFitnessReconciler{ConfigCache: NewConfigCache()}
	suite := &kubemootv1alpha1.CrewFitness{ObjectMeta: metav1.ObjectMeta{Name: "f", Namespace: "ns", Labels: map[string]string{
		suiteOwnerLabel: "s", suiteRunIDLabel: "run1"}}}
	job := r.buildJob(suite, "j", "http://gw", "k", "cm")
	wantNATSOptions(t, "suite runner", containerNamed(t, job.Spec.Template.Spec, componentFitnessRunner), job.Spec.Template.Spec)

	alone := &kubemootv1alpha1.CrewFitness{ObjectMeta: metav1.ObjectMeta{Name: "f", Namespace: "ns"}}
	job = r.buildJob(alone, "j", "http://gw", "k", "cm")
	wantNoNATSOptions(t, "standalone runner", containerNamed(t, job.Spec.Template.Spec, componentFitnessRunner))
	if len(job.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("a standalone runner mounts only its tests: %+v", job.Spec.Template.Spec.Volumes)
	}
}

func TestNATSSecretsAreCopiedIntoWorkloadNamespacesAndWatched(t *testing.T) {
	setNATSOptions(t)
	t.Setenv("OPERATOR_NAMESPACE", "kubemoot-system")
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	mk := func(name string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: operatorNamespace()},
			Data: map[string][]byte{"k": []byte("v")}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mk("nats-ca"), mk("nats-auth"), mk(unrelatedSecret)).Build()

	replicateNATSSecrets(context.Background(), c, testPlacementNS)

	for _, name := range []string{"nats-ca", "nats-auth"} {
		if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: testPlacementNS}, &corev1.Secret{}); err != nil {
			t.Errorf("%s was not copied: %v", name, err)
		}
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: unrelatedSecret, Namespace: testPlacementNS}, &corev1.Secret{}); err == nil {
		t.Error("only the NATS secrets are copied")
	}
	if !secretIsConfiguredSource(NewConfigCache(), mk("nats-ca")) || secretIsConfiguredSource(NewConfigCache(), mk(unrelatedSecret)) {
		t.Error("a change to a NATS secret must re-copy it, and only that")
	}
}

const unrelatedSecret = "unrelated"
