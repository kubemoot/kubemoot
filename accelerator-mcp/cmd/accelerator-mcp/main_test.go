package main

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/storetest"
)

func TestNewToolSet_wiresTheFiveTools(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = aiv1alpha1.AddToScheme(scheme)
	set := newToolSet(context.Background(), fake.NewClientBuilder().WithScheme(scheme).Build(), storetest.NewMemKV(), "kubemoot/accelerator-mcp/pod-a")
	if len(set.Specifications()) != 5 {
		t.Fatalf("five tools: %v", set.Specifications())
	}
	if set.Jobs == nil {
		t.Fatal("no background jobs runner for calibrate_provider")
	}
	if _, ok := set.Prober.(*probe.Prober); !ok {
		t.Fatalf("the prober the idle unloader drives: %T", set.Prober)
	}
	if set.Tickets.Holder != "kubemoot/accelerator-mcp/pod-a" || set.History.Agent != set.Tickets.Holder {
		t.Fatalf("probes write as the holder: %s %s", set.Tickets.Holder, set.History.Agent)
	}
	if id := holderID(); len(id) <= len(holderPrefix) || id[:len(holderPrefix)] != holderPrefix {
		t.Fatalf("holder id: %s", id)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("ACCEL_MCP_TEST", "x")
	if getenv("ACCEL_MCP_TEST", "y") != "x" || getenv("ACCEL_MCP_UNSET", "y") != "y" {
		t.Fatal("getenv")
	}
}
