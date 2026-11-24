package main

import (
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func TestTheGatewayStampsLabelsOnlyWhenTheOperatorSaysSo(t *testing.T) {
	env := func(v map[string]string) func(string) string { return func(k string) string { return v[k] } }
	for name, c := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"not set":      {map[string]string{}, false},
		"exact":        {map[string]string{identity.EnvExposeLabel: identity.EnvTrue}, true},
		"false":        {map[string]string{identity.EnvExposeLabel: "false"}, false},
		"not exactly":  {map[string]string{identity.EnvExposeLabel: "TRUE"}, false},
		"another name": {map[string]string{"ACCESS_EXPOSE": identity.EnvTrue}, false},
	} {
		if got := exposesLabel(env(c.env)); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
