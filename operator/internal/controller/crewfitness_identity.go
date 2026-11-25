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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// gatewayTokenVolume is the projected ServiceAccount token the fitness runner
	// sends to a crew's discussion gateway as "Authorization: Bearer".
	gatewayTokenVolume = "gateway-token"
	gatewayTokenDir    = "/var/run/secrets/kubemoot.ai/gateway"
	gatewayTokenFile   = gatewayTokenDir + "/" + credentialBrokerTokenFile
	// envGatewayTokenFile names the file the runner reads its token from.
	envGatewayTokenFile = "GATEWAY_TOKEN_FILE"
	// gatewayTokenSeconds is the token's lifetime; the kubelet renews it before it ends
	// and the runner reads the file on every request.
	gatewayTokenSeconds = int64(3600)

	// fitnessRefusedAnnotation is set on the Job by a runner whose crew refused it, with
	// the reason as the value. A refusal is not retried: asking again is refused again.
	fitnessRefusedAnnotation = "kubemoot.ai/fitness-refused"

	// crewsResource is the Kubemoot resource the runner's ServiceAccount may get.
	crewsResource = "crews"
)

// gatewayIdentity says how the fitness runner proves who it is to the crew under test.
type gatewayIdentity struct {
	// Audience is the audience the projected token is bound to; empty is the API
	// server's own, which is what a crew with no tokenAudiences accepts.
	Audience string
}

// crewRunnerRoleName is the Role that lets the fitness runner use one crew.
func crewRunnerRoleName(crew string) string {
	return componentFitnessRunner + "-" + crew
}

// identityFor is how the runner must identify itself to crew: nil when the crew's gateway
// verifies no Kubernetes token, so the runner asks as it always did. The audience is the
// crew's first tokenAudiences entry, which its gateway accepts.
func identityFor(crew *kubemootv1alpha1.Crew) *gatewayIdentity {
	access := discussionAccess(crew)
	if access == nil || !access.TokenReviewEnabled() {
		return nil
	}
	id := &gatewayIdentity{}
	if len(access.TokenAudiences) > 0 {
		id.Audience = access.TokenAudiences[0]
	}
	return id
}

// ensureRunnerCrewAccess lets the fitness runner's ServiceAccount get the one Crew under
// test, the access a gateway asks of every caller, and nothing else. The Role and its
// binding belong to the Crew, so they go with it.
func ensureRunnerCrewAccess(ctx context.Context, c client.Client, crew *kubemootv1alpha1.Crew) error {
	name := crewRunnerRoleName(crew.Name)
	labels := map[string]string{labelManagedBy: managedByValue, labelComponent: componentFitnessRunner}
	meta := func() metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: crew.Namespace} }

	role := &rbacv1.Role{ObjectMeta: meta()}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, role, func() error {
		role.Labels = mergeLabels(role.Labels, labels)
		role.Rules = []rbacv1.PolicyRule{{
			APIGroups:     []string{kubemootv1alpha1.GroupVersion.Group},
			Resources:     []string{crewsResource},
			ResourceNames: []string{crew.Name},
			Verbs:         []string{verbGet},
		}}
		return controllerutil.SetOwnerReference(crew, role, c.Scheme())
	}); err != nil {
		return fmt.Errorf("ensure role %s/%s: %w", crew.Namespace, name, err)
	}

	binding := &rbacv1.RoleBinding{ObjectMeta: meta()}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, binding, func() error {
		binding.Labels = mergeLabels(binding.Labels, labels)
		binding.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: componentFitnessRunner, Namespace: crew.Namespace}}
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}
		return controllerutil.SetOwnerReference(crew, binding, c.Scheme())
	}); err != nil {
		return fmt.Errorf("ensure role binding %s/%s: %w", crew.Namespace, name, err)
	}
	return nil
}

// withGatewayIdentity projects the runner's token into the pod and tells the runner where
// it is. A nil identity leaves the pod as it was.
func withGatewayIdentity(spec *corev1.PodSpec, id *gatewayIdentity) {
	if id == nil {
		return
	}
	expiry := gatewayTokenSeconds
	spec.Volumes = append(spec.Volumes, corev1.Volume{
		Name: gatewayTokenVolume,
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
				Audience: id.Audience, ExpirationSeconds: &expiry, Path: credentialBrokerTokenFile,
			}}},
		}},
	})
	for i := range spec.Containers {
		if spec.Containers[i].Name != componentFitnessRunner {
			continue
		}
		spec.Containers[i].VolumeMounts = append(spec.Containers[i].VolumeMounts,
			corev1.VolumeMount{Name: gatewayTokenVolume, MountPath: gatewayTokenDir, ReadOnly: true})
		spec.Containers[i].Env = append(spec.Containers[i].Env,
			corev1.EnvVar{Name: envGatewayTokenFile, Value: gatewayTokenFile})
	}
}

// crewIdentity gets the crew under test and, when its gateway verifies Kubernetes
// tokens, grants the runner access to it. It returns nil for a crew that verifies none,
// and for a crew that cannot be read: resolveEndpoint has already reported that.
func (r *CrewFitnessReconciler) crewIdentity(ctx context.Context, cf *kubemootv1alpha1.CrewFitness) (*gatewayIdentity, error) {
	crew := &kubemootv1alpha1.Crew{}
	if err := r.Get(ctx, client.ObjectKey{Name: cf.Spec.CrewRef, Namespace: cf.Namespace}, crew); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	id := identityFor(crew)
	if id == nil {
		return nil, nil
	}
	if err := ensureRunnerCrewAccess(ctx, r.Client, crew); err != nil {
		return nil, err
	}
	return id, nil
}
