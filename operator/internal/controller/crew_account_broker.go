/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// A crew whose askers rule is connectedAccount admits a person only while they hold a live
// connection, which the liaison keeps. The crew's gateway asks the liaison's account broker, and
// proves who it is with a projected ServiceAccount token made for that broker alone.
const (
	// AccountBrokerURLEnv is the operator's setting: the base address of the liaison's account
	// broker. Empty means the install keeps no connected accounts and no gateway is given one.
	AccountBrokerURLEnv = "KUBEMOOT_ACCOUNT_BROKER_URL"

	accountBrokerVolume    = "account-broker"
	accountBrokerMountDir  = "/var/run/secrets/kubemoot/account-broker"
	accountBrokerTokenFile = "token"
	// accountBrokerTokenSeconds is the lifetime of the projected token; the kubelet rotates it
	// and the gateway reads the file on every check.
	accountBrokerTokenSeconds = int64(3600)
)

// usesConnectedAccount reports whether the crew's rule is connectedAccount and can be applied.
func usesConnectedAccount(crew *kubemootv1alpha1.Crew) bool {
	access := discussionAccess(crew)
	return access != nil && access.Askers != nil && access.Askers.ConnectedAccount != "" &&
		len(access.TrustedHops) > 0 && validateTrustedHops(crew, access) == nil
}

// crewLeasesAccounts reports whether the crew declares connected accounts, so its gateway leases
// a person's token for them to the tool gateway.
func crewLeasesAccounts(crew *kubemootv1alpha1.Crew) bool {
	return len(crew.Spec.ConnectedAccounts) > 0 && crew.Spec.Discussion != nil && crew.Spec.Discussion.IsEnabled()
}

// crewLeases reports whether the crew's gateway answers the tool gateway's lease: it keeps
// people's tokens for the turn, or declares connected accounts.
func crewLeases(crew *kubemootv1alpha1.Crew) bool {
	return crewKeepsTokens(crew) || crewLeasesAccounts(crew)
}

// gatewayAccountBrokerEnv is the broker's address and the token file for a gateway that asks
// the broker: a connectedAccount crew's, or one that leases a declared account's token (which
// is also told its own name, to find a conversation's owner). Nothing when the install has no
// broker or the crew needs none.
func gatewayAccountBrokerEnv(crew *kubemootv1alpha1.Crew, brokerURL string) []corev1.EnvVar {
	brokerURL = strings.TrimSpace(brokerURL)
	if brokerURL == "" || (!usesConnectedAccount(crew) && !crewLeasesAccounts(crew)) {
		return nil
	}
	return []corev1.EnvVar{
		{Name: identity.EnvAccountBrokerURL, Value: brokerURL},
		{Name: identity.EnvAccountBrokerTokenFile, Value: accountBrokerMountDir + "/" + accountBrokerTokenFile},
		{Name: identity.EnvAccountCrew, Value: crew.Name},
	}
}

// applyAccountBroker projects the broker-audience token into the gateway's pod and mounts it
// read-only into the gateway container. It changes nothing for a crew that needs no broker.
func applyAccountBroker(podSpec *corev1.PodSpec, crew *kubemootv1alpha1.Crew, brokerURL string) {
	if len(gatewayAccountBrokerEnv(crew, brokerURL)) == 0 || len(podSpec.Containers) == 0 {
		return
	}
	seconds := accountBrokerTokenSeconds
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: accountBrokerVolume,
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
				Audience: identity.AccountBrokerAudience, ExpirationSeconds: &seconds, Path: accountBrokerTokenFile,
			}}},
		}},
	})
	c := &podSpec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: accountBrokerVolume, MountPath: accountBrokerMountDir, ReadOnly: true,
	})
	c.Env = append(c.Env, gatewayAccountBrokerEnv(crew, brokerURL)...)
}
