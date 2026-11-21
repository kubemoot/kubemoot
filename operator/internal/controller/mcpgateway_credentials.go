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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// callerCredentialPayload is the part of a server's registration that makes the gateway use
// the asking person's token. Nil for a server in mode Off, so the registration of every
// existing server is what it was.
func callerCredentialPayload(mcp *kubemootv1alpha1.MCPServer) map[string]interface{} {
	if !requiredCallerCredential(mcp) {
		return nil
	}
	cc := mcp.Spec.Registry.CallerCredential
	payload := map[string]interface{}{
		"mode":         string(cc.Mode),
		"verifier":     cc.Verifier,
		"relayContext": cc.RelayContext,
	}
	if cc.LeaseURL != "" {
		payload["leaseUrl"] = cc.LeaseURL
	}
	if cc.AccountName != "" {
		payload["accountName"] = cc.AccountName
	}
	if len(cc.OnForbidden) > 0 {
		payload["onForbidden"] = cc.OnForbidden
	}
	return payload
}

// anyCallerCredentialRequired reports whether any of the servers takes the person's own token.
func anyCallerCredentialRequired(servers []kubemootv1alpha1.MCPServer) bool {
	for i := range servers {
		if requiredCallerCredential(&servers[i]) {
			return true
		}
	}
	return false
}

// applyCredentialBroker lets a tool gateway lease people's tokens when a server it fronts is in
// mode Required: it runs as its own ServiceAccount, which the crew's gateway names as the one
// allowed to lease, carries a projected token for the broker's audience, and is told the
// address of the broker, the lease port of the namespace's one crew that keeps tokens. Without
// such a server, or on a gateway that is not Kubemoot's, nothing changes and an account left
// from before is removed.
func (r *MCPGatewayReconciler) applyCredentialBroker(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, d *appsv1.Deployment) error {
	if !isKubemootGateway(gateway) {
		return nil
	}
	servers, err := r.selectedServers(ctx, gateway)
	if err != nil {
		return err
	}
	if !anyCallerCredentialRequired(servers) {
		return r.removeBrokerAccount(ctx, gateway)
	}
	if err := r.ensureBrokerAccount(ctx, gateway); err != nil {
		return err
	}
	brokerURL, err := r.tokenCrewBrokerURL(ctx, gateway.Namespace)
	if err != nil {
		return err
	}
	podSpec := &d.Spec.Template.Spec
	podSpec.ServiceAccountName = credentialBrokerAccountName(gateway.Name)
	podSpec.Volumes = append(podSpec.Volumes, brokerTokenVolume())
	c := &podSpec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: credentialBrokerVolume, MountPath: credentialBrokerMountDir, ReadOnly: true,
	})
	if brokerURL != "" {
		c.Env = append(c.Env, corev1.EnvVar{Name: envCredentialBroker, Value: brokerURL})
	}
	return nil
}

// brokerTokenVolume projects a ServiceAccount token for the broker's audience, and no other.
func brokerTokenVolume() corev1.Volume {
	seconds := credentialBrokerTokenSeconds
	return corev1.Volume{
		Name: credentialBrokerVolume,
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
				Audience: credentialBrokerAudience, ExpirationSeconds: &seconds, Path: credentialBrokerTokenFile,
			}}},
		}},
	}
}

// tokenCrewBrokerURL is the lease address of the namespace's crew that keeps tokens, empty
// when there is none or more than one (the crews' CallerCredentials condition says so).
func (r *MCPGatewayReconciler) tokenCrewBrokerURL(ctx context.Context, namespace string) (string, error) {
	crews := &kubemootv1alpha1.CrewList{}
	if err := r.List(ctx, crews, client.InNamespace(namespace)); err != nil {
		return "", err
	}
	var holder *kubemootv1alpha1.Crew
	for i := range crews.Items {
		if !crewKeepsTokens(&crews.Items[i]) {
			continue
		}
		if holder != nil {
			return "", nil
		}
		holder = &crews.Items[i]
	}
	if holder == nil {
		return "", nil
	}
	return crewBrokerURL(holder), nil
}

func (r *MCPGatewayReconciler) ensureBrokerAccount(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: credentialBrokerAccountName(gateway.Name), Namespace: gateway.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = mergeLabels(sa.Labels, map[string]string{labelManagedBy: managedByValue, labelInstance: gateway.Name})
		return controllerutil.SetControllerReference(gateway, sa, r.Scheme)
	})
	return err
}

func (r *MCPGatewayReconciler) removeBrokerAccount(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	sa := &corev1.ServiceAccount{}
	key := client.ObjectKey{Name: credentialBrokerAccountName(gateway.Name), Namespace: gateway.Namespace}
	// Read first: a gateway that never used the broker costs no write.
	if err := r.Get(ctx, key, sa); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(sa, gateway) {
		return nil
	}
	if err := r.Delete(ctx, sa); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}
