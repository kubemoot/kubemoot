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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The liaison's account broker carries a person's account token and a gateway's ServiceAccount
// token, so it serves TLS. The broker is a separate process that never holds the install CA's
// key, so the operator signs its serving certificate with the install CA into a Secret in the
// operator's namespace, which the chart mounts into the liaison. The operator renews it before
// it ends and when the CA changes; the liaison reads the files again when they change. The crews'
// gateways verify the broker against the CA certificate the gateway's own certificate Secret carries.
const (
	// envBrokerTLSSecret is the Secret the broker's certificate goes in, and envBrokerService the
	// name of the broker's Service, which the certificate names. The chart sets both when
	// connected accounts and gateway TLS are on.
	envBrokerTLSSecret = "ACCOUNT_BROKER_TLS_SECRET"
	envBrokerService   = "ACCOUNT_BROKER_SERVICE_NAME"
)

var brokerTLSLog = logf.Log.WithName("account-broker-tls")

// BrokerCertificate issues the account broker's serving certificate from the install CA.
type BrokerCertificate struct {
	CA *InstallCA
	// Service is the broker's Service name; SecretName is the Secret that holds the certificate.
	Service, SecretName string
	// Every is how often the certificate is checked; zero is caPublishEvery.
	Every time.Duration
}

// BrokerCertificateFromEnv returns the issuer the environment turns on, or nil when there is no
// install CA or the chart named no broker.
func BrokerCertificateFromEnv(ca *InstallCA, getenv func(string) string) *BrokerCertificate {
	secret, service := getenv(envBrokerTLSSecret), getenv(envBrokerService)
	if ca == nil || secret == "" || service == "" {
		return nil
	}
	return &BrokerCertificate{CA: ca, Service: service, SecretName: secret}
}

// Ensure makes sure the Secret holds a current certificate for the broker Service's names.
func (b *BrokerCertificate) Ensure(ctx context.Context) error {
	key := client.ObjectKey{Namespace: b.CA.Namespace, Name: b.SecretName}
	_, err := b.CA.EnsureLeaf(ctx, key, serviceDNSNames(b.Service, b.CA.Namespace), nil)
	return err
}

// Start implements manager.Runnable: it issues the certificate at once and checks it again on a
// timer, so a renewal or a new CA reaches the broker without a restart.
func (b *BrokerCertificate) Start(ctx context.Context) error {
	return runEvery(ctx, b.Every, func(ctx context.Context) {
		if err := b.Ensure(ctx); err != nil {
			brokerTLSLog.Info("The account broker's certificate is not issued", "reason", err.Error())
		}
	})
}

// NeedLeaderElection is true: one operator replica issues the certificate.
func (b *BrokerCertificate) NeedLeaderElection() bool { return true }

// brokerCAFile is the CA file a crew's gateway verifies the broker against: the CA bundle its own
// gateway certificate Secret carries, which keeps the previous CA through a rotation so the
// broker verifies before and after it re-issues its certificate. Empty when gateways serve no TLS, so the pod has
// no such mount.
func (r *CrewReconciler) brokerCAFile() string {
	if r.GatewayTLS == nil {
		return ""
	}
	return gatewayTLSMount + "/" + secretKeyCABundle
}
