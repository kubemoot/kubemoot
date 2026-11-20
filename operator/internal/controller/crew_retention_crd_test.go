/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The Crew CRD carries the retention defaults once, and refuses a duration that is not in
// the Prometheus form. The behavior of a value is tested in internal/retention.
var _ = Describe("Crew retention declarations", func() {
	newCrew := func(name string, r *kubemootv1alpha1.CrewRetention) *kubemootv1alpha1.Crew {
		c := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: celNamespace}}
		c.Spec.Retention = r
		return c
	}
	create := func(c *kubemootv1alpha1.Crew) error {
		err := k8sClient.Create(ctx, c)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, c) })
		}
		return err
	}

	It("fills the defaults on a crew that declares nothing", func() {
		c := newCrew("ret-none", nil)
		Expect(create(c)).To(Succeed())
		r := c.Spec.Retention
		Expect(r).NotTo(BeNil())
		Expect(r.Schedule).To(Equal("@daily"))
		Expect(string(r.PurgeUnused)).To(Equal("90d"))
		Expect(r.Conversations).NotTo(BeNil())
		Expect(string(r.Conversations.Window)).To(Equal("30d"))
		Expect(r.Memory).NotTo(BeNil())
		Expect(r.Memory.PurgeUnused).To(BeEmpty(), "memory takes the crew-wide value unless it sets its own")
		Expect(r.Other).To(BeNil(), "other data keeps the streams' own limits unless a window is set")
	})

	It("keeps what a crew declares and defaults the rest", func() {
		c := newCrew("ret-some", &kubemootv1alpha1.CrewRetention{
			Memory: &kubemootv1alpha1.MemoryRetention{PurgeUnused: retentionNever},
			Other:  &kubemootv1alpha1.OtherRetention{Window: "1y6w"},
		})
		Expect(create(c)).To(Succeed())
		Expect(string(c.Spec.Retention.Memory.PurgeUnused)).To(Equal(retentionNever))
		Expect(string(c.Spec.Retention.Other.Window)).To(Equal("1y6w"))
		Expect(c.Spec.Retention.Schedule).To(Equal("@daily"))
		Expect(string(c.Spec.Retention.Conversations.Window)).To(Equal("30d"))
	})

	It("takes a cron schedule", func() {
		Expect(create(newCrew("ret-cron", &kubemootv1alpha1.CrewRetention{Schedule: "0 3 * * *"}))).To(Succeed())
	})

	for _, bad := range []kubemootv1alpha1.RetentionDuration{"30", "0d", "1d1y", "30 d", "forever", "1.5d", "Never", "1ms"} {
		It("refuses the duration "+string(bad), func() {
			err := create(newCrew("ret-bad", &kubemootv1alpha1.CrewRetention{PurgeUnused: bad}))
			Expect(err).To(HaveOccurred())
		})
	}
})
