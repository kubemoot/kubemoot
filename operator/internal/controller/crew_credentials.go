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
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// A person's own token reaches a tool in two hops that the operator wires, both off unless a
// Crew declares a serviceTokens verifier with passToTools: the crew's discussion gateway
// keeps the token in memory for the turn, and the crew's tool gateway leases it from the
// gateway's lease port for each call, proving who it is with a projected ServiceAccount token.
const (
	// credentialBrokerAudience is the audience of the tool gateway's projected token.
	credentialBrokerAudience = "kubemoot-credential-broker"
	// credentialBrokerMountDir and credentialBrokerTokenFile are where the tool gateway reads
	// that token; they match mcp.gateway.credential-broker.token-file.
	credentialBrokerMountDir  = "/var/run/secrets/kubemoot/credential-broker"
	credentialBrokerTokenFile = "token"
	credentialBrokerVolume    = "credential-broker"
	// credentialBrokerTokenSeconds is the lifetime of the projected token; the kubelet
	// rotates it and the gateway reads the file on every lease.
	credentialBrokerTokenSeconds = int64(3600)

	// The environment of the discussion gateway's lease listener, and of the tool gateway.
	envAccessBrokerSubject = "ACCESS_BROKER_SUBJECT"
	envInternalPort        = "INTERNAL_PORT"
	envCredentialBroker    = "CREDENTIAL_BROKER_URL"

	portNameInternal = "internal"

	defaultHashKeySecretKey = "hash-key"

	conditionTypeCallerCredentials = "CallerCredentials"
	reasonCallerCredentialsReady   = "Ready"
	reasonNoTokenPassingVerifier   = "NoVerifierPassesTokens"
	reasonUnknownVerifier          = "UnknownVerifier"
	reasonNoSingleToolGateway      = "NoSingleToolGateway"
	reasonSeveralTokenCrews        = "SeveralCrewsPassTokens"
)

// serviceTokenConfigs is the crew's verifiers in the form the gateway reads.
func serviceTokenConfigs(access *kubemootv1alpha1.DiscussionAccess) []identity.ServiceTokenConfig {
	if access == nil {
		return nil
	}
	out := make([]identity.ServiceTokenConfig, 0, len(access.ServiceTokens))
	for _, v := range access.ServiceTokens {
		out = append(out, identity.ServiceTokenConfig{
			Name: v.Name, VerifyURL: v.VerifyURL, VerifyMethod: v.VerifyMethod, SubjectClaim: v.SubjectClaim,
			Audiences: v.Audiences, MaxCacheSeconds: int(v.MaxCacheSeconds), PassToTools: v.PassToTools,
			Authorize: v.Authorize, Subjects: v.Subjects,
		})
	}
	return out
}

// gatewayServiceTokenEnv is the environment of the service token verifiers: the list, and the
// install's hash key from the Secret the crew names. Nothing for a crew with no verifier.
func gatewayServiceTokenEnv(access *kubemootv1alpha1.DiscussionAccess) []corev1.EnvVar {
	configs := serviceTokenConfigs(access)
	if len(configs) == 0 {
		return nil
	}
	raw, err := json.Marshal(configs)
	if err != nil { // a list of strings and numbers always marshals
		return nil
	}
	env := []corev1.EnvVar{{Name: identity.EnvServiceTokens, Value: string(raw)}}
	if name, key, ok := hashKeySecret(access); ok {
		env = append(env, corev1.EnvVar{Name: identity.EnvHashKey, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key},
		}})
	}
	return env
}

// gatewayServiceTokenCrewEnv names the crew a gateway's service token verifiers belong to, so a
// subscriber they accept is admitted for that crew alone. Nothing for a crew with no verifier.
func gatewayServiceTokenCrewEnv(crew *kubemootv1alpha1.Crew) []corev1.EnvVar {
	if len(serviceTokenConfigs(discussionAccess(crew))) == 0 {
		return nil
	}
	return []corev1.EnvVar{{Name: identity.EnvCrew, Value: identity.CrewRef{Namespace: crew.Namespace, Name: crew.Name}.String()}}
}

// hashKeySecret is the Secret and key that hold the crew's hash key, the one place the default
// key name is applied: the gateway reads the key from it and the operator reads the same one.
func hashKeySecret(access *kubemootv1alpha1.DiscussionAccess) (name, key string, ok bool) {
	if access == nil || access.HashKeySecretRef == nil {
		return "", "", false
	}
	ref := access.HashKeySecretRef
	key = ref.Key
	if key == "" {
		key = defaultHashKeySecretKey
	}
	return ref.Name, key, true
}

// passesTokens reports whether any verifier has the gateway keep the accepted token for tools.
func passesTokens(access *kubemootv1alpha1.DiscussionAccess) bool {
	if access == nil {
		return false
	}
	for i := range access.ServiceTokens {
		if access.ServiceTokens[i].PassToTools {
			return true
		}
	}
	return false
}

// crewKeepsTokens reports whether the crew's gateway runs and keeps people's tokens.
func crewKeepsTokens(crew *kubemootv1alpha1.Crew) bool {
	return crew.Spec.Discussion != nil && crew.Spec.Discussion.IsEnabled() && passesTokens(crew.Spec.Discussion.Access)
}

// credentialBrokerAccountName is the ServiceAccount of a tool gateway that leases tokens. It
// is the gateway's own, so the lease can name that one account and no other pod's.
func credentialBrokerAccountName(gatewayName string) string {
	return gatewayName + "-credential-broker"
}

// serviceAccountUser is the user name the API server gives a ServiceAccount's token.
func serviceAccountUser(namespace, name string) string {
	return "system:serviceaccount:" + namespace + ":" + name
}

// crewBrokerURL is the address of the crew's lease port, on its discussion gateway Service.
func crewBrokerURL(crew *kubemootv1alpha1.Crew) string {
	return "http://" + crewGatewayName(crew) + "." + crew.Namespace + ".svc:" + strconv.Itoa(int(netpol.DiscussionGatewayLeasePort))
}

// toolGatewayAccount is the ServiceAccount the crew's tool gateway leases with: the one a
// single MCPGateway in the namespace gets. With none or several there is none to name.
func toolGatewayAccount(ctx context.Context, c client.Reader, namespace string) (string, error) {
	gateways := &kubemootv1alpha1.MCPGatewayList{}
	if err := c.List(ctx, gateways, client.InNamespace(namespace)); err != nil {
		return "", err
	}
	kubemootGateways := kubemootGatewaysOf(gateways.Items)
	if len(kubemootGateways) != 1 {
		return "", nil
	}
	return credentialBrokerAccountName(kubemootGateways[0].Name), nil
}

// isKubemootGateway reports whether the gateway is Kubemoot's own, the only one that leases.
func isKubemootGateway(gw *kubemootv1alpha1.MCPGateway) bool {
	impl := gw.Spec.Implementation
	return impl == "" || impl == kubemootv1alpha1.ImplementationKubemoot
}

func kubemootGatewaysOf(items []kubemootv1alpha1.MCPGateway) []*kubemootv1alpha1.MCPGateway {
	var out []*kubemootv1alpha1.MCPGateway
	for i := range items {
		if isKubemootGateway(&items[i]) {
			out = append(out, &items[i])
		}
	}
	return out
}

// gatewayFronts reports whether the gateway registers the namespace's server: the same
// selector test the gateway applies when it lists its servers.
func gatewayFronts(gw *kubemootv1alpha1.MCPGateway, s *kubemootv1alpha1.MCPServer) bool {
	return selectorMatches(gw.Spec.MCPServerSelector, s.Labels, true)
}

// applyCredentialLease makes the discussion gateway of a crew that keeps tokens or declares
// connected accounts listen for leases:
// the pod carries the label its network policy keys on, the lease port is declared, and the
// one ServiceAccount allowed to lease is named. The subject is empty when the namespace has no
// single tool gateway, which leaves the listener off. A crew with nothing to lease is untouched.
func (r *CrewReconciler) applyCredentialLease(ctx context.Context, crew *kubemootv1alpha1.Crew, d *appsv1.Deployment) error {
	if !crewLeases(crew) {
		return nil
	}
	account, err := toolGatewayAccount(ctx, r.Client, crew.Namespace)
	if err != nil {
		return err
	}
	d.Spec.Template.Labels = mergeLabels(d.Spec.Template.Labels, map[string]string{netpol.LabelCredentialLease: netpol.CredentialLeaseValue})
	c := &d.Spec.Template.Spec.Containers[0]
	c.Ports = append(c.Ports, corev1.ContainerPort{
		Name: portNameInternal, ContainerPort: netpol.DiscussionGatewayLeasePort, Protocol: corev1.ProtocolTCP,
	})
	if account != "" {
		c.Env = append(c.Env,
			corev1.EnvVar{Name: envAccessBrokerSubject, Value: serviceAccountUser(crew.Namespace, account)},
			corev1.EnvVar{Name: envInternalPort, Value: strconv.Itoa(int(netpol.DiscussionGatewayLeasePort))})
	}
	return nil
}

// takesPersonsToken reports whether a server takes the asking person's own token, from a
// service token verifier or from a connected account.
func takesPersonsToken(s *kubemootv1alpha1.MCPServer) bool {
	return s.Spec.Registry != nil && s.Spec.Registry.CallerCredential != nil &&
		s.Spec.Registry.CallerCredential.Mode == kubemootv1alpha1.CallerCredentialRequired
}

// requiredCallerCredential reports whether a server takes the asking person's token from a
// crew's service token verifier: the tokens the crew's discussion gateway keeps and its tool
// gateway leases. A server that takes a connected account's token is not one; its crew's
// ConnectedAccounts condition describes it.
func requiredCallerCredential(s *kubemootv1alpha1.MCPServer) bool {
	return takesPersonsToken(s) && s.Spec.Registry.CallerCredential.Account == ""
}

// reconcileAccessConditions records the conditions that describe who may ask the crew and
// whether its tools can act as the asker. They are informational: a failure to read the
// namespace is logged and leaves the last condition in place, and never fails the reconcile.
func (r *CrewReconciler) reconcileAccessConditions(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	reconcileAuthentication(crew, r.AskerPolicyHosts)
	if err := r.reconcileCallerCredentials(ctx, crew); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to check the crew's caller credentials", "crew", crew.Name)
	}
	if err := r.reconcileConnectedAccounts(ctx, crew); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to check the crew's connected accounts", "crew", crew.Name)
	}
}

// reconcileCallerCredentials records the CallerCredentials condition of a crew whose namespace
// has a tool server in mode Required: True when a verifier of the crew passes the person's
// token to tools and the pieces that carry it exist, False with the reason when one is
// missing, so a server that can never get a token is visible. A crew in a namespace with no
// such server carries no condition.
func (r *CrewReconciler) reconcileCallerCredentials(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	required, err := r.requiredCredentialServers(ctx, crew.Namespace)
	if err != nil {
		return err
	}
	if len(required) == 0 || (crew.Spec.Discussion != nil && !crew.Spec.Discussion.IsEnabled()) {
		meta.RemoveStatusCondition(&crew.Status.Conditions, conditionTypeCallerCredentials)
		return nil
	}
	status, reason, message, err := r.callerCredentialState(ctx, crew, required)
	if err != nil {
		return err
	}
	setCrewCondition(crew, conditionTypeCallerCredentials, status, reason, message)
	return nil
}

// requiredCredentialServers names the namespace's servers in mode Required, sorted.
func (r *CrewReconciler) requiredCredentialServers(ctx context.Context, namespace string) ([]*kubemootv1alpha1.MCPServer, error) {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, servers, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	gateways := &kubemootv1alpha1.MCPGatewayList{}
	if err := r.List(ctx, gateways, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	toolGateways := kubemootGatewaysOf(gateways.Items)
	var out []*kubemootv1alpha1.MCPServer
	for i := range servers.Items {
		if requiredCallerCredential(&servers.Items[i]) && frontedByAny(toolGateways, &servers.Items[i]) {
			out = append(out, &servers.Items[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// frontedByAny reports whether a tool gateway registers the server; with no tool gateway at all
// every server counts, so the condition can say that none exists.
func frontedByAny(gateways []*kubemootv1alpha1.MCPGateway, s *kubemootv1alpha1.MCPServer) bool {
	if len(gateways) == 0 {
		return true
	}
	for _, gw := range gateways {
		if gatewayFronts(gw, s) {
			return true
		}
	}
	return false
}

// callerCredentialState judges the wiring one check at a time, most basic first.
func (r *CrewReconciler) callerCredentialState(ctx context.Context, crew *kubemootv1alpha1.Crew,
	required []*kubemootv1alpha1.MCPServer) (metav1.ConditionStatus, string, string, error) {
	access := discussionAccess(crew)
	if !passesTokens(access) {
		return metav1.ConditionFalse, reasonNoTokenPassingVerifier, fmt.Sprintf(
			"%s takes the asking person's own token, and no serviceTokens verifier of this crew has passToTools, "+
				"so no call to it can carry one.", required[0].Name), nil
	}
	for _, s := range required {
		if name := s.Spec.Registry.CallerCredential.Verifier; !passesTokensFrom(access, name) {
			return metav1.ConditionFalse, reasonUnknownVerifier, fmt.Sprintf(
				"%s takes tokens from verifier %q, which is not a serviceTokens entry of this crew with passToTools.",
				s.Name, name), nil
		}
	}
	return r.callerCredentialWiring(ctx, crew)
}

// callerCredentialWiring checks that the namespace has one tool gateway to lease and that this
// is the only crew of the namespace that keeps tokens, because the tool gateway has one broker.
func (r *CrewReconciler) callerCredentialWiring(ctx context.Context, crew *kubemootv1alpha1.Crew) (metav1.ConditionStatus, string, string, error) {
	account, err := toolGatewayAccount(ctx, r.Client, crew.Namespace)
	if err != nil {
		return "", "", "", err
	}
	if account == "" {
		return metav1.ConditionFalse, reasonNoSingleToolGateway,
			"The namespace needs exactly one MCPGateway to lease the person's token; it has none or several.", nil
	}
	crews := &kubemootv1alpha1.CrewList{}
	if err := r.List(ctx, crews, client.InNamespace(crew.Namespace)); err != nil {
		return "", "", "", err
	}
	for i := range crews.Items {
		if other := &crews.Items[i]; other.Name != crew.Name && crewKeepsTokens(other) {
			return metav1.ConditionFalse, reasonSeveralTokenCrews, fmt.Sprintf(
				"Crew %s also keeps tokens; the namespace's tool gateway leases from one crew, so keep tokens in one crew per namespace.",
				other.Name), nil
		}
	}
	return metav1.ConditionTrue, reasonCallerCredentialsReady,
		"The crew's gateway keeps the asking person's token for the turn and the tool gateway may lease it for each call.", nil
}

// passesTokensFrom reports whether the named verifier passes tokens to tools.
func passesTokensFrom(access *kubemootv1alpha1.DiscussionAccess, name string) bool {
	for i := range access.ServiceTokens {
		if access.ServiceTokens[i].Name == name {
			return access.ServiceTokens[i].PassToTools
		}
	}
	return false
}

// enqueueCrewsInNamespace reconciles every Crew of the namespace of a tool server or a tool
// gateway, because its CallerCredentials condition and its gateway's lease subject follow them.
func enqueueCrewsInNamespace(cli client.Reader) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		crews := &kubemootv1alpha1.CrewList{}
		if err := cli.List(ctx, crews, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(crews.Items))
		for i := range crews.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&crews.Items[i])})
		}
		return reqs
	})
}
