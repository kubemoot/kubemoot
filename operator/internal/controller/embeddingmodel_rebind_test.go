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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// slowPullState records what the slow Ollama saw of a download: that it began, and
// that the operator cancelled it.
type slowPullState struct {
	started   atomic.Bool
	cancelled atomic.Bool
}

// slowPullOllama is an Ollama server with no models whose downloads never finish
// until the test ends; it records a download starting and one the operator cancels.
func slowPullOllama(t *testing.T, seen *slowPullState) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPathAPITags:
			_, _ = w.Write([]byte(`{"models":[]}`))
		case testPathAPIPull:
			seen.started.Store(true)
			emit(w, halfDone)
			<-r.Context().Done()
			seen.cancelled.Store(true)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startDeclaredPull binds the model to a server a Model declares and waits for the
// download to be under way on the server. The first reconcile returns before the
// request reaches the server, and a download cancelled before then is never seen
// by it, so the wait is on the server having started the download.
func startDeclaredPull(t *testing.T, seen *slowPullState) (*bindHarness, *aiv1alpha1.Model) {
	t.Helper()
	slow := slowPullOllama(t, seen)
	ask := embedModelFor("a-slow")
	h := newBindHarness(t, unboundEmbedding(), embedServer("a-slow", slow.URL, false), ask)
	got := h.reconcile()
	if got.Status.State != statePulling || got.Status.Pull == nil {
		t.Fatalf("status = %+v, want Pulling with progress", got.Status)
	}
	eventually(t, "the download to start on the server", seen.started.Load)
	return h, ask
}

func TestEmbeddingBinding_RebindReleasesThePullOnTheServerItLeft(t *testing.T) {
	var seen slowPullState
	h, ask := startDeclaredPull(t, &seen)
	has := newEmbedOllama(t, true)
	if err := h.r.Create(context.Background(), embedServer("b-has", has.srv.URL, true)); err != nil {
		t.Fatal(err)
	}
	if err := h.r.Delete(context.Background(), ask); err != nil {
		t.Fatal(err)
	}

	got := h.reconcile()

	if got.Status.BoundServer != "b-has" || got.Status.Pull != nil || !got.Status.Ready {
		t.Errorf("status = %+v, want bound to b-has, ready, with no pull progress", got.Status)
	}
	eventually(t, "the download on the server the model left to be cancelled", seen.cancelled.Load)
	if h.r.tracker().get(pullKey("a-slow", embedTestModel)) != nil {
		t.Error("the pull on the old server is still tracked")
	}
}

func TestEmbeddingBinding_NoServerReleasesTheUnwantedPull(t *testing.T) {
	var seen slowPullState
	h, ask := startDeclaredPull(t, &seen)
	if err := h.r.Delete(context.Background(), ask); err != nil {
		t.Fatal(err)
	}

	got := h.reconcile()

	if got.Status.BoundServer != "" || got.Status.Ready {
		t.Errorf("status = %+v, want unbound once no Model asks for the model", got.Status)
	}
	eventually(t, "the unwanted download to be cancelled", seen.cancelled.Load)
}

func TestEmbeddingBinding_ADifferentTagOfTheNameIsNotTheModel(t *testing.T) {
	has := newEmbedOllama(t, true) // holds nomic-embed-text:latest
	em := unboundEmbedding()
	em.Spec.Model = embedTestModel + ":v2"
	server := embedServer("a-has", has.srv.URL, false)
	server.Status.EmbeddingModels = []aiv1alpha1.EmbeddingCatalogModel{{Name: embedTestModel + ":v2", Dimensions: embedTestDims}}
	h := newBindHarness(t, em, server)

	got := h.reconcile()

	if got.Status.Ready || got.Status.State != statePending {
		t.Errorf("status = %+v, want Pending: the server holds :latest, not :v2", got.Status)
	}
	if has.pulls.Load() != 0 {
		t.Error("no Model asks for :v2, so nothing is downloaded")
	}
}

func TestEmbeddingBinding_ATaglessNameIsTheLatestTag(t *testing.T) {
	has := newEmbedOllama(t, true) // holds nomic-embed-text:latest
	h := newBindHarness(t, unboundEmbedding(), embedServer("a-has", has.srv.URL, true))

	if got := h.reconcile(); !got.Status.Ready || got.Status.BoundServer != "a-has" {
		t.Errorf("status = %+v, want ready on a-has", got.Status)
	}
}

func TestServerDeclaresHonorsTheCatalogsDisabledRule(t *testing.T) {
	server := embedServer("a", "http://a", false)
	ask := embedModelFor("a")
	off := embedModelFor("a")
	off.Name = "a-embed-off"
	off.Spec.Disabled = true
	if !serverDeclares(server, embedTestModel, []aiv1alpha1.Model{*ask}) {
		t.Error("a Model on the server for the model declares it")
	}
	if serverDeclares(server, embedTestModel, []aiv1alpha1.Model{*ask, *off}) {
		t.Error("a disabled duplicate wins in the catalog, so the model is not declared")
	}
	other := embedModelFor("elsewhere")
	if serverDeclares(server, embedTestModel, []aiv1alpha1.Model{*other}) {
		t.Error("a Model for another server declares nothing here")
	}
}
