package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("coordinator-resolver")

// cachedEndpoint holds a resolved coordinator endpoint with TTL.
type cachedEndpoint struct {
	endpoint  string
	expiresAt time.Time
}

// Resolver looks up coordinator Agent CRs by crew label with a TTL cache.
type Resolver struct {
	client   client.Client
	cacheTTL time.Duration

	mu    sync.RWMutex
	cache map[string]cachedEndpoint
}

// NewResolver creates a coordinator resolver with the given cache TTL.
func NewResolver(c client.Client, cacheTTL time.Duration) *Resolver {
	return &Resolver{
		client:   c,
		cacheTTL: cacheTTL,
		cache:    make(map[string]cachedEndpoint),
	}
}

// Resolve returns the HTTP endpoint for the coordinator of the given crew.
// Results are cached for cacheTTL duration.
func (r *Resolver) Resolve(ctx context.Context, crew string) (string, error) {
	// Check cache first
	r.mu.RLock()
	if cached, ok := r.cache[crew]; ok && time.Now().Before(cached.expiresAt) {
		r.mu.RUnlock()
		return cached.endpoint, nil
	}
	r.mu.RUnlock()

	// Cache miss or expired — query K8s API
	var agents kubemootv1alpha1.AgentList
	err := r.client.List(ctx, &agents,
		client.MatchingLabels{
			"kubemoot.ai/crew": crew,
			"kubemoot.ai/role": "coordinator",
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to list coordinator agents for crew %q: %w", crew, err)
	}

	if len(agents.Items) == 0 {
		return "", fmt.Errorf("no coordinator found for crew %q", crew)
	}

	// Use the first ready coordinator, or fall back to the first one
	var endpoint string
	for _, agent := range agents.Items {
		if agent.Status.Ready && agent.Status.Endpoint != "" {
			endpoint = agent.Status.Endpoint
			break
		}
	}
	if endpoint == "" {
		// Fall back to first agent's endpoint
		endpoint = agents.Items[0].Status.Endpoint
		if endpoint == "" {
			return "", fmt.Errorf("coordinator for crew %q has no endpoint", crew)
		}
	}

	// Update cache
	r.mu.Lock()
	r.cache[crew] = cachedEndpoint{
		endpoint:  endpoint,
		expiresAt: time.Now().Add(r.cacheTTL),
	}
	r.mu.Unlock()

	log.Info("Resolved coordinator", "crew", crew, "endpoint", endpoint)
	return endpoint, nil
}

// Invalidate removes the cached endpoint for a crew.
func (r *Resolver) Invalidate(crew string) {
	r.mu.Lock()
	delete(r.cache, crew)
	r.mu.Unlock()
}
