/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

import (
	"strings"
	"testing"
)

func TestServerTicketPrefix(t *testing.T) {
	if got := ServerTicketPrefix("gpu-a"); got != "gpu-a__" {
		t.Errorf("prefix: %q", got)
	}
	// A server whose name starts another's must not match the other's tickets.
	if strings.HasPrefix("gpu-ab__1", ServerTicketPrefix("gpu-a")) {
		t.Error("gpu-a's prefix matches gpu-ab's tickets")
	}
}
