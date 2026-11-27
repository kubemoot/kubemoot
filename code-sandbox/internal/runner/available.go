package runner

import (
	"context"
	"fmt"
)

// ConfinementAvailable runs a trivial program the way every program runs and returns why it could
// not be confined, or nil when this host gives the sandbox the user namespaces and mounts it needs.
func (r *Runner) ConfinementAvailable(ctx context.Context) error {
	if r.Unconfined {
		return fmt.Errorf("confinement is turned off")
	}
	res, err := r.Execute(ctx, "bash", "true")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("a trivial program exited %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}
