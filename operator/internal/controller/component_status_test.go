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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func mkModelProvider(name string, ready bool) *kubemootv1alpha1.ModelProvider {
	return &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kubemoot"},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: ready},
	}
}

func findStatus(ss []ComponentStatus, name string) (ComponentStatus, bool) {
	for _, s := range ss {
		if s.Name == name {
			return s, true
		}
	}
	return ComponentStatus{}, false
}

// TestBuildComponentStatuses covers the operator-owned control-plane health:
// operator is always up (it answered), nats reflects the passed connectivity,
// and modelproviders is derived from CR status — never a false negative.
func TestBuildComponentStatuses(t *testing.T) {
	ctx := context.Background()

	t.Run("nats connected, mixed providers", func(t *testing.T) {
		c := newPropagationClient(t, mkModelProvider("gpu", true), mkModelProvider("rig1", false))
		ss := buildComponentStatuses(ctx, c, true)
		if op, _ := findStatus(ss, "operator"); !op.Healthy {
			t.Errorf("operator should be healthy (it answered)")
		}
		if n, _ := findStatus(ss, "nats"); !n.Healthy {
			t.Errorf("nats should be healthy when connected")
		}
		mp, ok := findStatus(ss, "modelproviders")
		if !ok || !mp.Healthy || mp.Message != "1/2 ready" {
			t.Errorf("modelproviders = %+v, want healthy '1/2 ready'", mp)
		}
	})

	t.Run("nats down, no providers is neutral", func(t *testing.T) {
		c := newPropagationClient(t)
		ss := buildComponentStatuses(ctx, c, false)
		if n, _ := findStatus(ss, "nats"); n.Healthy {
			t.Errorf("nats should be unhealthy when not connected")
		}
		mp, _ := findStatus(ss, "modelproviders")
		if !mp.Healthy || mp.Message != "none configured" {
			t.Errorf("no providers should be neutral-healthy 'none configured', got %+v", mp)
		}
	})

	t.Run("all providers down is unhealthy", func(t *testing.T) {
		c := newPropagationClient(t, mkModelProvider("gpu", false), mkModelProvider("rig1", false))
		ss := buildComponentStatuses(ctx, c, true)
		mp, _ := findStatus(ss, "modelproviders")
		if mp.Healthy || mp.Message != "0/2 ready" {
			t.Errorf("all-down providers should be unhealthy '0/2 ready', got %+v", mp)
		}
	})
}
