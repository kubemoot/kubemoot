package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

// busServer names the MCPServer and report the bus tests reconcile.
const busServer = "bus-srv"

// busCount counts messages per subject on a throwaway NATS server.
type busCount struct {
	mu     sync.Mutex
	counts map[string]int
	pub    *kubemootnats.Publisher
}

func newBusCount(t *testing.T) *busCount {
	t.Helper()
	env := natstest.Start(t, natstest.Options{})
	pub := kubemootnats.NewPublisher(env.URL)
	t.Cleanup(pub.Close)
	b := &busCount{counts: map[string]int{}, pub: pub}
	sub, err := pub.Subscribe(">", func(m *nats.Msg) {
		b.mu.Lock()
		b.counts[m.Subject]++
		b.mu.Unlock()
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return b
}

// count waits for the bus to settle, then returns the messages seen on the subject.
func (b *busCount) count(subject string) int {
	_ = b.pub.Connected()
	time.Sleep(150 * time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts[subject]
}

func busFixture(t *testing.T) (*MCPServerReconciler, *kubemootv1alpha1.MCPServer, *busCount) {
	t.Helper()
	scheme := gatewayScheme(t)
	ms := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: busServer, Namespace: testNS1},
		Spec:       kubemootv1alpha1.MCPServerSpec{Image: specImage, Transport: kubemootv1alpha1.TransportStdio},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ms).WithStatusSubresource(ms, &kubemootv1alpha1.MCPServerReport{}).Build()
	bus := newBusCount(t)
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), NATSPublisher: bus.pub}
	return r, ms, bus
}

func TestMCPServerStatusPublishedOnlyWhenItChanges(t *testing.T) {
	ctx := context.Background()
	r, ms, bus := busFixture(t)
	const subject = "kubemoot.operator.mcpserver.bus-srv.status"

	for i := 0; i < 3; i++ {
		if _, err := r.updateStatus(ctx, ms, "Running", true, "all good"); err != nil {
			t.Fatal(err)
		}
	}
	if got := bus.count(subject); got != 1 {
		t.Fatalf("three reconciles with no change published %d status messages, want 1", got)
	}
	if _, err := r.updateStatus(ctx, ms, "Failed", false, "pod crashed"); err != nil {
		t.Fatal(err)
	}
	if got := bus.count(subject); got != 2 {
		t.Fatalf("a change must publish once more, got %d messages, want 2", got)
	}

	// An operator restart forgets the last payload: the same state publishes once.
	restarted := newBusCount(t)
	r2 := &MCPServerReconciler{Client: r.Client, Scheme: r.Scheme, ConfigCache: NewConfigCache(), NATSPublisher: restarted.pub}
	for i := 0; i < 2; i++ {
		if _, err := r2.updateStatus(ctx, ms, "Failed", false, "pod crashed"); err != nil {
			t.Fatal(err)
		}
	}
	if got := restarted.count(subject); got != 1 {
		t.Fatalf("after a restart the state must publish once, got %d", got)
	}
}

func TestMCPServerTrialPublishedOnlyWhenItChanges(t *testing.T) {
	ctx := context.Background()
	r, ms, bus := busFixture(t)
	const subject = "kubemoot.chronicle.bus-srv.trial"

	for i := 0; i < 3; i++ {
		r.recordDeploymentTrial(ctx, ms, true, "deployed")
	}
	if got := bus.count(subject); got != 1 {
		t.Fatalf("an unchanged trial published %d messages, want 1", got)
	}
	r.recordDeploymentTrial(ctx, ms, false, "image pull failed")
	if got := bus.count(subject); got != 2 {
		t.Fatalf("a changed trial must publish once more, got %d, want 2", got)
	}
}

func TestMCPServerReportVerdictPublishedOnlyWhenItChanges(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	report := &kubemootv1alpha1.MCPServerReport{
		ObjectMeta: metav1.ObjectMeta{Name: busServer, Namespace: testNS1},
		Spec:       kubemootv1alpha1.MCPServerReportSpec{ServerName: busServer},
		Status: kubemootv1alpha1.MCPServerReportStatus{
			Trials: []kubemootv1alpha1.TrialRecord{{Success: true}, {Success: true}, {Success: true}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(report).WithStatusSubresource(report).Build()
	bus := newBusCount(t)
	r := &MCPServerReportReconciler{Client: cli, Scheme: scheme, NATSPublisher: bus.pub}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: busServer, Namespace: testNS1}}
	const subject = "kubemoot.chronicle.bus-srv.verdict"

	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if got := bus.count(subject); got != 1 {
		t.Fatalf("three reconciles with no change published %d verdicts, want 1", got)
	}

	if err := cli.Get(ctx, req.NamespacedName, report); err != nil {
		t.Fatal(err)
	}
	report.Status.Trials = []kubemootv1alpha1.TrialRecord{{Success: false}, {Success: false}, {Success: false}}
	if err := cli.Status().Update(ctx, report); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := bus.count(subject); got != 2 {
		t.Fatalf("a changed verdict must publish once more, got %d, want 2", got)
	}
}
