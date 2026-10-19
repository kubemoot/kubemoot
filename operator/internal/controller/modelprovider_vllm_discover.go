/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine/vllm"
)

// serviceKey is the Service a serviceRef names, in the provider's namespace when
// the reference gives none.
func serviceKey(provider *aiv1alpha1.ModelProvider) types.NamespacedName {
	ref := provider.Spec.ServiceRef
	ns := ref.Namespace
	if ns == "" {
		ns = provider.Namespace
	}
	return types.NamespacedName{Namespace: ns, Name: ref.Name}
}

// discoverVLLMServers reads the Service a provider's serviceRef names and asks each
// of its ports which model it serves (the first model its /v1/models lists). The
// result is one server per port, in port order, at http://<service>.<namespace>.svc:<port>.
// A port that does not answer right now keeps the server the provider found there
// before, because a server restarting is not a model renamed; a port that never
// answered, or serves a name an earlier port serves, is left out. Each port that was
// left out or kept over is described in notes.
func discoverVLLMServers(ctx context.Context, c client.Reader, httpClient *http.Client, provider *aiv1alpha1.ModelProvider) (servers []aiv1alpha1.VLLMServer, notes []string, err error) {
	key := serviceKey(provider)
	svc := &corev1.Service{}
	if err := c.Get(ctx, key, svc); err != nil {
		return nil, nil, fmt.Errorf("reading Service %s: %w", key, err)
	}
	ports := append([]corev1.ServicePort(nil), svc.Spec.Ports...)
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })

	adapter := vllm.New(httpClient)
	seen := map[string]bool{}
	for _, p := range ports {
		endpoint := fmt.Sprintf("http://%s.%s.svc:%d", svc.Name, svc.Namespace, p.Port)
		srv, note := readPort(ctx, adapter, endpoint, provider.Status.VLLMServers)
		if note != "" {
			notes = append(notes, fmt.Sprintf("port %d %s", p.Port, note))
		}
		switch {
		case srv.Model == "":
		case seen[srv.Model]:
			notes = append(notes, fmt.Sprintf("port %d serves %q, which an earlier port serves", p.Port, srv.Model))
		default:
			seen[srv.Model] = true
			servers = append(servers, srv)
		}
	}
	return servers, notes, nil
}

// readPort asks the server at endpoint which model it serves. When it does not
// answer it returns the server previously found at that endpoint, if any, with a note
// saying so; with none it returns no server and a note.
func readPort(ctx context.Context, adapter *vllm.Adapter, endpoint string, previous []aiv1alpha1.VLLMServer) (aiv1alpha1.VLLMServer, string) {
	models, err := adapter.ServedModels(ctx, endpoint)
	if err == nil && len(models) > 0 {
		return aiv1alpha1.VLLMServer{Model: models[0].ID, Endpoint: endpoint}, ""
	}
	note := "serves no model yet"
	if err != nil {
		note = fmt.Sprintf("did not answer: %v", err)
	}
	for _, prev := range previous {
		if prev.Endpoint == endpoint {
			return prev, note + "; keeping " + prev.Model
		}
	}
	return aiv1alpha1.VLLMServer{}, note
}

// servicesToProviders maps a Service to the providers whose serviceRef names it, so
// a Service that gains or loses ports is discovered again at once.
func (r *ModelProviderReconciler) servicesToProviders(ctx context.Context, obj client.Object) []reconcile.Request {
	providers := &aiv1alpha1.ModelProviderList{}
	if err := r.List(ctx, providers); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range providers.Items {
		p := &providers.Items[i]
		if p.Spec.ServiceRef != nil && serviceKey(p) == client.ObjectKeyFromObject(obj) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		}
	}
	return out
}
