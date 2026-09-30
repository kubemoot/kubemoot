package liaison

import (
	"context"
	"fmt"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Crew is what a client may learn about a crew: where it is and what it is for.
type Crew struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Description string `json:"description,omitempty"`
	Ready       bool   `json:"ready"`
}

// Lister finds the crews a liaison may front.
type Lister interface {
	List(ctx context.Context) ([]Crew, error)
}

type k8sLister struct {
	reader client.Reader
}

// NewLister lists Crew resources across the cluster that have a discussion gateway.
func NewLister(reader client.Reader) Lister {
	return &k8sLister{reader: reader}
}

func (l *k8sLister) List(ctx context.Context) ([]Crew, error) {
	var crews kubemootv1alpha1.CrewList
	if err := l.reader.List(ctx, &crews); err != nil {
		return nil, fmt.Errorf("listing crews: %w", err)
	}
	out := make([]Crew, 0, len(crews.Items))
	for i := range crews.Items {
		c := &crews.Items[i]
		if !hasGateway(c) {
			continue
		}
		out = append(out, Crew{
			Name:        c.Name,
			Namespace:   c.Namespace,
			Description: c.Spec.Description,
			Ready:       c.Status.Ready,
		})
	}
	return out, nil
}

// hasGateway is true when the crew deploys a discussion gateway (the CRD default).
func hasGateway(c *kubemootv1alpha1.Crew) bool {
	return c.Spec.Discussion == nil || c.Spec.Discussion.Enabled
}

// find resolves a crew by name, and by namespace when the name is not unique.
func find(crews []Crew, name, namespace string) (Crew, error) {
	var matches []Crew
	for _, c := range crews {
		if c.Name == name && (namespace == "" || c.Namespace == namespace) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Crew{}, fmt.Errorf("no crew named %q%s; call list_crews for the crews available", name, inNamespace(namespace))
	default:
		return Crew{}, fmt.Errorf("crew %q exists in %d namespaces; pass namespace to choose one", name, len(matches))
	}
}

func inNamespace(namespace string) string {
	if namespace == "" {
		return ""
	}
	return " in namespace " + namespace
}
