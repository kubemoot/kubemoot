package placement

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestErrorTextsNameTheCause(t *testing.T) {
	if got := (&ErrNoFit{Reason: "no room"}).Error(); got != "kubemoot_no_fit: no room" {
		t.Fatalf("ErrNoFit = %q", got)
	}
	cause := errors.New("connection refused")
	err := &ErrUnavailable{Err: cause}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("ErrUnavailable = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("ErrUnavailable does not unwrap to its cause")
	}
}

type planningPlacer struct {
	Placer
	got PlanRequest
	err error
}

func (p *planningPlacer) Plan(_ context.Context, r PlanRequest) error {
	p.got = r
	return p.err
}

func TestFallbackPlanGoesToAPlanningPrimaryOnly(t *testing.T) {
	want := errors.New("plan refused")
	p := &planningPlacer{err: want}
	if err := (Fallback{Primary: p}).Plan(context.Background(), PlanRequest{Crew: "c", Agent: "a"}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want the primary's", err)
	}
	if p.got.Crew != "c" || p.got.Agent != "a" {
		t.Fatalf("primary got %+v", p.got)
	}
	// A primary that cannot plan, and a bound agent, have nothing to plan.
	if err := (Fallback{Primary: Bound{}}).Plan(context.Background(), PlanRequest{}); err != nil {
		t.Fatalf("non-planner primary: %v", err)
	}
	if err := (Bound{}).Plan(context.Background(), PlanRequest{}); err != nil {
		t.Fatalf("bound: %v", err)
	}
}
