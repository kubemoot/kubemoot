/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/util/workqueue"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func configWithRuntimeClass(name, runtimeClass string) *kubemootv1alpha1.KubemootConfig {
	cfg := &kubemootv1alpha1.KubemootConfig{ObjectMeta: metav1.ObjectMeta{Name: name}}
	cfg.Spec.Defaults.AgentRuntimeClassName = runtimeClass
	return cfg
}

// After the refresh an Agent resolves the new realm default, though the KubemootConfigReconciler
// has not yet loaded it into the cache.
func TestRefreshConfigCache_AgentSeesTheNewRuntimeClassAtOnce(t *testing.T) {
	live := configWithRuntimeClass(DefaultKubemootConfigName, "gvisor")
	cli := newPropagationClient(t, live)
	cache := NewConfigCache()
	cache.Update(configWithRuntimeClass(DefaultKubemootConfigName, ""))
	r := &AgentReconciler{Client: cli, ConfigCache: cache}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "k8sgpt", Namespace: testNS1}}

	if got := r.agentRuntimeClassName(agent); got != "" {
		t.Fatalf("precondition: the stale cache should give no class, got %q", got)
	}
	refreshConfigCache(context.Background(), cli, cache, live)
	if got := r.agentRuntimeClassName(agent); got != "gvisor" {
		t.Errorf("after the refresh the Agent should resolve gvisor, got %q", got)
	}
}

func TestRefreshConfigCache_DeletedConfigClearsTheCache(t *testing.T) {
	cli := newPropagationClient(t)
	cache := NewConfigCache()
	cache.Update(configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))

	refreshConfigCache(context.Background(), cli, cache, configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))
	if got := cache.GetAgentRuntimeClassName(); got != "" {
		t.Errorf("a deleted KubemootConfig should clear the cache, got %q", got)
	}
}

func TestRefreshConfigCache_IgnoresOtherObjects(t *testing.T) {
	cli := newPropagationClient(t, configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))
	cache := NewConfigCache()

	refreshConfigCache(context.Background(), cli, cache, configWithRuntimeClass("experimental", "other"))
	refreshConfigCache(context.Background(), cli, cache, &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName}})
	if cache.GetConfig() != nil {
		t.Error("only the default KubemootConfig may fill the cache")
	}
}

func TestRefreshConfigCache_NilCacheIsANoOp(t *testing.T) {
	cli := newPropagationClient(t, configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))
	refreshConfigCache(context.Background(), cli, nil, configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))
}

func TestRefreshConfigCache_ReadFailureKeepsTheCache(t *testing.T) {
	base := newPropagationClient(t, configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))
	failing := fake.NewClientBuilder().WithScheme(base.Scheme()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("apiserver unavailable")
		},
	}).Build()
	cache := NewConfigCache()
	cache.Update(configWithRuntimeClass(DefaultKubemootConfigName, "kata"))

	refreshConfigCache(context.Background(), failing, cache, configWithRuntimeClass(DefaultKubemootConfigName, "gvisor"))
	if got := cache.GetAgentRuntimeClassName(); got != "kata" {
		t.Errorf("a failed read must leave the cache as it was, got %q", got)
	}
}

// The watch handler refreshes the cache before it maps the event to requests, so the reconciles
// it raises read the changed config.
func TestKubemootConfigHandlerRefreshesTheCacheBeforeMapping(t *testing.T) {
	live := configWithRuntimeClass(DefaultKubemootConfigName, "gvisor")
	cli := newPropagationClient(t, live, &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: testNS1}})
	cache := NewConfigCache()
	h := enqueueAllOnKubemootConfigChange(cli, cache, func() client.ObjectList { return &kubemootv1alpha1.AgentList{} }, "agent")

	q := &recordingQueue{}
	h.Create(context.Background(), event.CreateEvent{Object: live}, q)
	if len(q.added) != 1 {
		t.Fatalf("expected the Agent enqueued, got %d", len(q.added))
	}
	if got := cache.GetAgentRuntimeClassName(); got != "gvisor" {
		t.Errorf("the cache should hold the changed config once the handler ran, got %q", got)
	}
}

// recordingQueue records what a handler adds.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	added []reconcile.Request
}

func (q *recordingQueue) Add(r reconcile.Request) { q.added = append(q.added, r) }
