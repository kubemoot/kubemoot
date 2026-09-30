/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestValidateSinkSpec(t *testing.T) {
	cases := []struct {
		name       string
		spec       kubemootv1alpha1.NotificationSinkSpec
		wantReason string
	}{
		{
			name:       "missing URL",
			spec:       kubemootv1alpha1.NotificationSinkSpec{},
			wantReason: "MissingURL",
		},
		{
			name: "bad scheme",
			spec: kubemootv1alpha1.NotificationSinkSpec{
				Webhook: kubemootv1alpha1.WebhookTarget{URL: "ftp://example.com/x"},
			},
			wantReason: "InvalidURL",
		},
		{
			name: "missing host",
			spec: kubemootv1alpha1.NotificationSinkSpec{
				Webhook: kubemootv1alpha1.WebhookTarget{URL: "https://"},
			},
			wantReason: "InvalidURL",
		},
		{
			name: "header with both value and valueFrom",
			spec: kubemootv1alpha1.NotificationSinkSpec{
				Webhook: kubemootv1alpha1.WebhookTarget{
					URL: "https://ntfy.example.com/topic",
					Headers: []kubemootv1alpha1.HeaderEntry{
						{
							Name:      "Authorization",
							Value:     "Bearer x",
							ValueFrom: &corev1.EnvVarSource{},
						},
					},
				},
			},
			wantReason: "InvalidHeader",
		},
		{
			name: "header missing both",
			spec: kubemootv1alpha1.NotificationSinkSpec{
				Webhook: kubemootv1alpha1.WebhookTarget{
					URL:     "https://ntfy.example.com/topic",
					Headers: []kubemootv1alpha1.HeaderEntry{{Name: "X-Bad"}},
				},
			},
			wantReason: "InvalidHeader",
		},
		{
			name: "valid spec",
			spec: kubemootv1alpha1.NotificationSinkSpec{
				Webhook: kubemootv1alpha1.WebhookTarget{
					URL: "https://ntfy.example.com/topic",
					Headers: []kubemootv1alpha1.HeaderEntry{
						{Name: "X-Tag", Value: "homelab"},
					},
				},
			},
			wantReason: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, _ := validateSinkSpec(&tc.spec)
			if reason != tc.wantReason {
				t.Errorf("got reason=%q, want %q", reason, tc.wantReason)
			}
		})
	}
}
