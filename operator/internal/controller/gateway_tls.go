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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// A crew's discussion gateway carries callers' bearer tokens, so it serves TLS. The operator
// signs a serving certificate for each crew's gateway Service with the install CA into a Secret
// in the crew's namespace (labelled so the CA bundle keeps a previous CA while any certificate
// still depends on it); the dashboard and the crew liaison verify the gateway against the CA
// bundle the operator publishes.
const (
	// gatewayTLSPort is the container port of the gateway's TLS listener.
	gatewayTLSPort = int32(netpol.DiscussionGatewayTLSPort)
	// gatewayTLSServicePort is the Service port clients use, so https://<crew>-discussion needs no port.
	gatewayTLSServicePort = int32(443)
	// gatewayTLSMount is where the serving certificate and key are mounted in the gateway pod.
	gatewayTLSMount = "/etc/kubemoot/gateway-tls"

	portNameHTTPS = "https"

	// gatewayCertCheckEvery is how often a crew is reconciled again to check its certificate, so
	// a renewal does not wait for another change to the crew. The check is cheap and the renewal
	// window is a third of a year.
	gatewayCertCheckEvery = 12 * time.Hour

	// conditionTypeGatewayTLSReady is the Crew condition for the gateway's serving certificate; its
	// message names when the certificate ends.
	conditionTypeGatewayTLSReady = "GatewayTLSReady"
)

// Environment the chart sets on the operator for gateway TLS.
const (
	envGatewayTLSEnabled = "GATEWAY_TLS_ENABLED"
	envGatewayTLSPlain   = "GATEWAY_TLS_PLAIN_LISTENER"
)

var gatewayTLSLog = logf.Log.WithName("gateway-tls")

// GatewayTLS issues the discussion gateways' serving certificates from the install CA. A nil
// *GatewayTLS means the install serves its gateways over plain http only.
type GatewayTLS struct {
	CA *InstallCA
	// PlainListener keeps the gateways' plain listener beside the TLS one.
	PlainListener bool
}

// GatewayTLSFromEnv returns the issuer the environment turns on, or nil when there is no install
// CA or the chart turned gateway TLS off.
func GatewayTLSFromEnv(ca *InstallCA, getenv func(string) string) *GatewayTLS {
	if ca == nil || getenv(envGatewayTLSEnabled) != valueTrue {
		return nil
	}
	return &GatewayTLS{CA: ca, PlainListener: getenv(envGatewayTLSPlain) != valueFalse}
}

// gatewayTLSSecretName is the Secret holding a crew gateway's serving certificate.
func gatewayTLSSecretName(gatewayName string) string { return gatewayName + "-tls" }

// EnsureCert makes sure the crew's gateway has a current serving certificate in its namespace,
// owned by the crew so it goes when the crew does. It returns when that certificate ends.
func (g *GatewayTLS) EnsureCert(
	ctx context.Context, crew *kubemootv1alpha1.Crew, gatewayName string, scheme *runtime.Scheme,
) (time.Time, error) {
	key := client.ObjectKey{Namespace: crew.Namespace, Name: gatewayTLSSecretName(gatewayName)}
	own := func(secret *corev1.Secret) error { return controllerutil.SetControllerReference(crew, secret, scheme) }
	return g.CA.EnsureLeaf(ctx, key, serviceDNSNames(gatewayName, crew.Namespace), own)
}

// reconcileGatewayCert issues or renews the crew gateway's serving certificate when the install
// serves gateways over TLS, and records the outcome in the crew's GatewayTLSReady condition. A
// failure (a CA that is expired or near its end, one that cannot be read) is an event on the crew.
func (r *CrewReconciler) reconcileGatewayCert(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	if r.GatewayTLS == nil {
		// An install that no longer serves TLS must not keep a condition saying it does.
		meta.RemoveStatusCondition(&crew.Status.Conditions, conditionTypeGatewayTLSReady)
		return nil
	}
	notAfter, err := r.GatewayTLS.EnsureCert(ctx, crew, r.gatewayName(crew), r.Scheme)
	if err != nil {
		setCrewCondition(crew, conditionTypeGatewayTLSReady, metav1.ConditionFalse, "CertificateNotIssued", err.Error())
		if r.Recorder != nil {
			r.Recorder.Eventf(crew, nil, corev1.EventTypeWarning, "GatewayTLSFailed", "Issue", "The gateway's TLS certificate could not be issued: %v", err)
		}
		return err
	}
	setCrewCondition(crew, conditionTypeGatewayTLSReady, metav1.ConditionTrue, "CertificateIssued",
		fmt.Sprintf("The gateway's certificate is valid until %s", notAfter.UTC().Format(time.RFC3339)))
	return nil
}

// apply adds the TLS listener to a gateway pod: the certificate volume, the port, the settings
// the gateway reads, and probes over https (the plain listener may be off). A nil issuer leaves
// the pod as it was.
func (g *GatewayTLS) apply(spec *corev1.PodSpec, gatewayName string) {
	if g == nil || len(spec.Containers) == 0 {
		return
	}
	const volume = "gateway-tls"
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: volume, VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: gatewayTLSSecretName(gatewayName)},
	}})
	c := &spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: volume, MountPath: gatewayTLSMount, ReadOnly: true})
	c.Ports = append(c.Ports, corev1.ContainerPort{Name: portNameHTTPS, ContainerPort: gatewayTLSPort, Protocol: corev1.ProtocolTCP})
	c.Env = append(c.Env,
		corev1.EnvVar{Name: "TLS_CERT_FILE", Value: gatewayTLSMount + "/" + corev1.TLSCertKey},
		corev1.EnvVar{Name: "TLS_KEY_FILE", Value: gatewayTLSMount + "/" + corev1.TLSPrivateKeyKey},
		corev1.EnvVar{Name: "TLS_PORT", Value: fmt.Sprintf("%d", gatewayTLSPort)},
		corev1.EnvVar{Name: "PLAIN_LISTENER", Value: fmt.Sprintf("%t", g.PlainListener)},
	)
	for _, probe := range []*corev1.Probe{c.LivenessProbe, c.ReadinessProbe} {
		if probe != nil && probe.HTTPGet != nil {
			probe.HTTPGet.Port = intstr.FromInt32(gatewayTLSPort)
			probe.HTTPGet.Scheme = corev1.URISchemeHTTPS
		}
	}
}

// withCertCheck makes a reconcile that finished healthy come back, so the crew's certificate
// is checked and renewed in time.
func (r *CrewReconciler) withCertCheck(res ctrl.Result, crew *kubemootv1alpha1.Crew) ctrl.Result {
	if r.GatewayTLS == nil || crew.Spec.Discussion == nil || !crew.Spec.Discussion.IsEnabled() {
		return res
	}
	if res.RequeueAfter == 0 || res.RequeueAfter > gatewayCertCheckEvery {
		res.RequeueAfter = gatewayCertCheckEvery
	}
	return res
}

// enqueueCrewsOnCA queues every crew when the CA Secret changes, so each gets a certificate the
// new CA signed at once.
func enqueueCrewsOnCA(cli client.Reader) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return allCrewRequests(ctx, cli)
	})
}

// allCrewRequests is a reconcile request for every crew.
func allCrewRequests(ctx context.Context, cli client.Reader) []reconcile.Request {
	crews := &kubemootv1alpha1.CrewList{}
	if err := cli.List(ctx, crews); err != nil {
		gatewayTLSLog.Error(err, "could not list crews after the install CA changed")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(crews.Items))
	for i := range crews.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&crews.Items[i])})
	}
	return reqs
}

// caSecretPredicate lets through the install CA Secret when it appears or its content changes,
// and nothing else, so no other Secret event queues a crew.
func caSecretPredicate(ca *InstallCA) predicate.Predicate {
	isCA := func(o client.Object) bool {
		return ca != nil && o.GetNamespace() == ca.Namespace && o.GetName() == ca.SecretName
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isCA(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isCA(e.ObjectNew) && secretContentChanged(e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// installCA is the CA the reconciler's gateways are signed with, or nil with no gateway TLS.
func (r *CrewReconciler) installCA() *InstallCA {
	if r.GatewayTLS == nil {
		return nil
	}
	return r.GatewayTLS.CA
}

// labelledInstallCALeaf lets through events of the Secrets that hold a certificate the install CA
// signed, so no other Secret a crew owns wakes the crew's reconcile.
func labelledInstallCALeaf() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.GetLabels()[LabelInstallCALeaf]
		return ok
	})
}
