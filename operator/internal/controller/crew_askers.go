/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	reasonInvalidTrustedHops = "InvalidTrustedHops"
	// reasonUntrustedPolicyHost is the Authentication reason of a crew whose external askers
	// rule names a host the install does not list.
	reasonUntrustedPolicyHost = "UntrustedPolicyHost"
	// AskerPolicyHostsEnv is the operator's setting: the policy decision point hosts, comma
	// separated, that crews' external askers rules may use. Empty trusts none.
	AskerPolicyHostsEnv = "DISCUSSION_GATEWAY_ASKER_POLICY_HOSTS"
)

// AskerPolicyHostsFromEnv reads AskerPolicyHostsEnv. rejected holds the entries that are not
// plain host names, which are never trusted.
func AskerPolicyHostsFromEnv(getenv func(string) string) (hosts identity.PolicyHosts, rejected []string) {
	return identity.ParsePolicyHosts(getenv(AskerPolicyHostsEnv))
}

// validateAskerPolicyHost reports why the crew's external askers rule cannot be applied on this
// install, or nil: its policy decision point's host must be one the install lists, because the
// gateway sends each asker's identity there and the crew author is not the one who decides where.
func validateAskerPolicyHost(access *kubemootv1alpha1.DiscussionAccess, hosts identity.PolicyHosts) error {
	if access == nil || access.Askers == nil || access.Askers.External == nil {
		return nil
	}
	if !hosts.Trusts(access.Askers.External.URL) {
		return fmt.Errorf("askers.external.url names a host this install does not trust as a policy decision point; "+
			"ask the install's operator to list it in %s", AskerPolicyHostsEnv)
	}
	return nil
}

// validateTrustedHops reports why the crew's trusted hops and askers rule cannot be applied,
// or nil. The gateway is then given none of them, so no assertion is accepted: a mistake
// closes the door and never opens it.
func validateTrustedHops(crew *kubemootv1alpha1.Crew, access *kubemootv1alpha1.DiscussionAccess) error {
	if access == nil || len(access.TrustedHops) == 0 {
		return nil
	}
	if err := validateHopKeys(access.TrustedHops); err != nil {
		return err
	}
	if access.Askers == nil {
		return fmt.Errorf("trustedHops needs askers, the rule that decides who may ask")
	}
	rule := askersConfigOf(access.Askers)
	if err := rule.Validate(); err != nil {
		return err
	}
	if _, _, ok := hashKeySecret(access); !ok && access.Askers.KubernetesRBAC == nil {
		return fmt.Errorf("askers other than kubernetesRBAC need hashKeySecretRef")
	}
	return declaredAccount(crew, access.Askers)
}

// validateHopKeys checks that every listed key is an ECDSA P-256 public key in PEM.
func validateHopKeys(hops []kubemootv1alpha1.TrustedHop) error {
	for _, h := range hops {
		for i, pemText := range h.PublicKeys {
			if _, err := identity.ParseHopPublicKey(pemText); err != nil {
				return fmt.Errorf("trustedHops %q: publicKeys[%d] is not an ECDSA P-256 public key in PEM (%v)", h.Name, i, err)
			}
		}
	}
	return nil
}

// declaredAccount checks that a connectedAccount rule names an account the crew declares.
func declaredAccount(crew *kubemootv1alpha1.Crew, rule *kubemootv1alpha1.AskerRule) error {
	if rule.ConnectedAccount == "" {
		return nil
	}
	if !slices.ContainsFunc(crew.Spec.ConnectedAccounts, func(a kubemootv1alpha1.ConnectedAccount) bool {
		return a.Name == rule.ConnectedAccount
	}) {
		return fmt.Errorf("askers.connectedAccount %q is not in spec.connectedAccounts", rule.ConnectedAccount)
	}
	return nil
}

// invalidTrustedHops records the Authentication condition False when the hops cannot be
// applied, and reports whether it did.
func invalidTrustedHops(crew *kubemootv1alpha1.Crew, access *kubemootv1alpha1.DiscussionAccess, hosts identity.PolicyHosts) bool {
	reason := reasonInvalidTrustedHops
	err := validateTrustedHops(crew, access)
	if err == nil && access != nil && len(access.TrustedHops) > 0 {
		if err = validateAskerPolicyHost(access, hosts); err != nil {
			reason = reasonUntrustedPolicyHost
		}
	}
	if err == nil {
		return false
	}
	setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionFalse, reason,
		"No assertion of who is asking is accepted until this is fixed: "+err.Error()+".")
	return true
}

// hopsAuthentication records the Authentication condition of a crew whose door people are
// admitted by a service token or a trusted hop, and reports whether it did. A trusted hop that
// cannot be applied is reported first, because it closes the door.
func hopsAuthentication(crew *kubemootv1alpha1.Crew, access *kubemootv1alpha1.DiscussionAccess, hosts identity.PolicyHosts) bool {
	return invalidTrustedHops(crew, access, hosts) || serviceTokenAuthentication(crew, access) ||
		trustedHopAuthentication(crew, access)
}

// askersConfigOf is the rule as the gateway reads it.
func askersConfigOf(rule *kubemootv1alpha1.AskerRule) identity.AskersConfig {
	c := identity.AskersConfig{ConnectedAccount: rule.ConnectedAccount, IssuerGroups: rule.IssuerGroups}
	if rule.KubernetesRBAC != nil {
		c.KubernetesRBAC = &struct{}{}
	}
	if rule.AnySignedIn != nil {
		c.AnySignedIn = &struct{}{}
	}
	if e := rule.External; e != nil {
		c.External = &identity.ExternalConfig{URL: e.URL, SubjectType: e.SubjectType, ResourceType: e.ResourceType, Action: e.Action}
	}
	return c
}

// gatewayTrustedHopEnv is the list of doors whose assertions the gateway accepts, each with
// the audience of this crew's gateway alone, so an assertion signed for one crew is refused by
// every other, and the rule that decides who of their people may ask. Nothing for a crew that
// lists no hop or whose hops cannot be applied.
func gatewayTrustedHopEnv(crew *kubemootv1alpha1.Crew, hosts identity.PolicyHosts) []corev1.EnvVar {
	access := discussionAccess(crew)
	if access == nil || len(access.TrustedHops) == 0 || validateTrustedHops(crew, access) != nil ||
		validateAskerPolicyHost(access, hosts) != nil {
		return nil
	}
	hops := make([]identity.TrustedHopConfig, 0, len(access.TrustedHops))
	for _, h := range access.TrustedHops {
		hops = append(hops, identity.TrustedHopConfig{
			Name: h.Name, Audience: identity.HopAudience(crew.Namespace, crew.Name), PublicKeys: h.PublicKeys,
		})
	}
	rule := askersConfigOf(access.Askers)
	rule.Namespace, rule.Crew = crew.Namespace, crew.Name
	hopsJSON, err1 := json.Marshal(hops)
	ruleJSON, err2 := json.Marshal(rule)
	if err1 != nil || err2 != nil { // lists of strings and plain structs always marshal
		return nil
	}
	env := []corev1.EnvVar{{Name: identity.EnvTrustedHops, Value: string(hopsJSON)}, {Name: identity.EnvAskers, Value: string(ruleJSON)}}
	env = append(env, gatewayAskersKeyEnv(access)...)
	env = append(env, gatewayAskersPolicyHostsEnv(access, hosts)...)
	return append(env, gatewayAskersTokenEnv(access)...)
}

// gatewayAskersKeyEnv hands the gateway the install's hash key when a rule that does not map
// to Kubernetes needs it, and the crew declares no service token that already does.
func gatewayAskersKeyEnv(access *kubemootv1alpha1.DiscussionAccess) []corev1.EnvVar {
	name, key, ok := hashKeySecret(access)
	if !ok || access.Askers.KubernetesRBAC != nil || len(access.ServiceTokens) > 0 {
		return nil
	}
	return []corev1.EnvVar{secretEnv(identity.EnvHashKey, name, key)}
}

// gatewayAskersPolicyHostsEnv hands the gateway the install's trusted policy decision point
// hosts, which it checks again before it sends anyone's identity to the rule's host.
func gatewayAskersPolicyHostsEnv(access *kubemootv1alpha1.DiscussionAccess, hosts identity.PolicyHosts) []corev1.EnvVar {
	if access.Askers.External == nil {
		return nil
	}
	return []corev1.EnvVar{{Name: identity.EnvAskersPolicyHosts, Value: strings.Join(hosts, ",")}}
}

// gatewayAskersTokenEnv hands the gateway the token for an external policy decision point.
func gatewayAskersTokenEnv(access *kubemootv1alpha1.DiscussionAccess) []corev1.EnvVar {
	e := access.Askers.External
	if e == nil || e.TokenSecretRef == nil {
		return nil
	}
	return []corev1.EnvVar{secretEnv(identity.EnvAskersToken, e.TokenSecretRef.Name, e.TokenSecretRef.Key)}
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key,
	}}}
}

// trustedHopAuthentication is the Authentication condition of a crew that accepts a trusted
// hop's assertion of who is asking, and false when it lists none.
func trustedHopAuthentication(crew *kubemootv1alpha1.Crew, access *kubemootv1alpha1.DiscussionAccess) bool {
	if access == nil || len(access.TrustedHops) == 0 {
		return false
	}
	names := make([]string, 0, len(access.TrustedHops))
	for _, h := range access.TrustedHops {
		names = append(names, h.Name)
	}
	msg := "The assertion of who is asking from " + strings.Join(names, ", ") + " is verified. " +
		askersSentence(access.Askers) + " Conversations belong to that person."
	if access.TokenReviewEnabled() {
		msg += " Kubernetes tokens are verified too."
	}
	if access.AnonymousAllowed() {
		msg += " Callers with no token are also accepted, as anonymous."
	}
	setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionTrue, reasonTrustedHop, msg)
	return true
}

// askersSentence says who of the asserted people may ask.
func askersSentence(rule *kubemootv1alpha1.AskerRule) string {
	switch {
	case rule.KubernetesRBAC != nil:
		return "Each person needs get on the Crew in Kubernetes RBAC."
	case rule.ConnectedAccount != "":
		return "Each person the door signed in may ask; the crew's tools need their connected account " + rule.ConnectedAccount + "."
	case len(rule.IssuerGroups) > 0:
		return "Each person needs one of the groups " + strings.Join(rule.IssuerGroups, ", ") + " from the sign-in provider."
	case rule.External != nil:
		return "An external policy decision point decides for each person."
	}
	return "Each person the door signed in may ask."
}
