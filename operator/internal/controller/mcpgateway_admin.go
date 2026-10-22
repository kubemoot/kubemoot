/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The Kubemoot gateway serves registration and the other mutating admin routes on their
// own port, behind a bearer token the operator generates per MCPGateway. The tool routes
// and the agent-readable tool lists stay on the gateway port.
const (
	defaultGatewayAdminPort = int32(8081)
	// fallbackGatewayAdminPort is used when spec.port already claims the default admin port.
	fallbackGatewayAdminPort = int32(8082)
	portNameAdmin            = "admin"
	envAdminPort             = "MCP_GATEWAY_ADMIN_PORT"
	envAdminToken            = "MCP_GATEWAY_ADMIN_TOKEN"
	gatewayAdminTokenKey     = "token"
	gatewayAdminTokenBytes   = 32
	controlPlaneLabel        = "control-plane"
	controllerManagerValue   = "controller-manager"
	namespaceNameLabel       = "kubernetes.io/metadata.name"
	adminTokenHashAnnotation = "kubemoot.ai/admin-token-hash"
	adminTokenHashLen        = 16
)

// gatewayImplementation returns the implementation of a gateway, defaulting to Kubemoot.
func gatewayImplementation(gateway *kubemootv1alpha1.MCPGateway) kubemootv1alpha1.MCPGatewayImplementation {
	if gateway.Spec.Implementation == "" {
		return kubemootv1alpha1.ImplementationKubemoot
	}
	return gateway.Spec.Implementation
}

// gatewayHasAdminPort reports whether the gateway splits its admin routes onto their own port.
func gatewayHasAdminPort(gateway *kubemootv1alpha1.MCPGateway) bool {
	return gatewayImplementation(gateway) == kubemootv1alpha1.ImplementationKubemoot
}

// gatewayToolPort is the port that serves tool calls and the tool lists.
func gatewayToolPort(gateway *kubemootv1alpha1.MCPGateway) int32 {
	if gateway.Spec.Port != 0 {
		return gateway.Spec.Port
	}
	return defaultGatewayPort
}

// gatewayAdminPort is the port the operator calls for registration: the admin port of a
// Kubemoot gateway, the single port of any other implementation.
func gatewayAdminPort(gateway *kubemootv1alpha1.MCPGateway) int32 {
	tool := gatewayToolPort(gateway)
	if !gatewayHasAdminPort(gateway) {
		return tool
	}
	if tool == defaultGatewayAdminPort {
		return fallbackGatewayAdminPort
	}
	return defaultGatewayAdminPort
}

// gatewayAdminSecretName names the Secret holding a gateway's admin token.
func gatewayAdminSecretName(gateway *kubemootv1alpha1.MCPGateway) string {
	return gateway.Name + "-admin-token"
}

// gatewayAdminEnv is the environment that opens the admin port and hands the gateway its token.
func gatewayAdminEnv(gateway *kubemootv1alpha1.MCPGateway) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: envAdminPort, Value: fmt.Sprintf("%d", gatewayAdminPort(gateway))},
		{Name: envAdminToken, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: gatewayAdminSecretName(gateway)},
				Key:                  gatewayAdminTokenKey,
			},
		}},
	}
}

// generateAdminToken returns a random URL-safe token.
func generateAdminToken() (string, error) {
	raw := make([]byte, gatewayAdminTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating the gateway admin token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// secretReader reads through the uncached API reader when one is set, so a token Secret
// the cache has not seen yet is never mistaken for a missing one.
func (r *MCPGatewayReconciler) secretReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// adminTokenHash is a short fingerprint of the token (never the token) for the pod template.
func adminTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:adminTokenHashLen]
}

// ensureAdminTokenSecret creates the gateway's admin token Secret once and reuses it on
// every later reconcile. The Secret is owned by the MCPGateway.
func (r *MCPGatewayReconciler) ensureAdminTokenSecret(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	key := types.NamespacedName{Namespace: gateway.Namespace, Name: gatewayAdminSecretName(gateway)}
	existing := &corev1.Secret{}
	err := r.secretReader().Get(ctx, key, existing)
	if err == nil && len(existing.Data[gatewayAdminTokenKey]) > 0 {
		return nil
	}
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	token, err := generateAdminToken()
	if err != nil {
		return err
	}
	if existing.Name != "" {
		return r.fillAdminToken(ctx, existing, token)
	}
	return r.createAdminTokenSecret(ctx, gateway, key, token)
}

// fillAdminToken gives a Secret that lost its token a new one; its other content stays.
func (r *MCPGatewayReconciler) fillAdminToken(ctx context.Context, secret *corev1.Secret, token string) error {
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[gatewayAdminTokenKey] = []byte(token)
	return r.Update(ctx, secret)
}

// createAdminTokenSecret writes the gateway's admin token Secret, owned by the MCPGateway.
// When another reconcile created it first, the existing token is kept.
func (r *MCPGatewayReconciler) createAdminTokenSecret(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, key types.NamespacedName, token string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: gatewayLabels(gateway)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{gatewayAdminTokenKey: []byte(token)},
	}
	if err := controllerutil.SetControllerReference(gateway, secret, r.Scheme); err != nil {
		return err
	}
	log := logf.FromContext(ctx)
	err := r.Create(ctx, secret)
	if err == nil {
		log.Info("Created the gateway admin token Secret", "secret", key.Name)
		return nil
	}
	if !errors.IsAlreadyExists(err) {
		return err
	}
	if _, readErr := r.kubemootAdminToken(ctx, gateway); readErr != nil {
		return fmt.Errorf("the admin token Secret already exists but cannot be used: %w", readErr)
	}
	log.Info("The gateway admin token Secret already exists, using its token", "secret", key.Name)
	return nil
}

// gatewayLabels are the labels every object a gateway owns carries.
func gatewayLabels(gateway *kubemootv1alpha1.MCPGateway) map[string]string {
	return map[string]string{
		labelName:      gateway.Name,
		labelInstance:  gateway.Name,
		labelManagedBy: managedByValue,
		labelComponent: componentMCPGateway,
	}
}

// kubemootAdminToken reads the generated admin token the operator presents to the gateway.
func (r *MCPGatewayReconciler) kubemootAdminToken(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (string, error) {
	secret := &corev1.Secret{}
	if err := r.secretReader().Get(ctx, types.NamespacedName{Namespace: gateway.Namespace, Name: gatewayAdminSecretName(gateway)}, secret); err != nil {
		return "", fmt.Errorf("failed to get the gateway admin token secret: %w", err)
	}
	token := secret.Data[gatewayAdminTokenKey]
	if len(token) == 0 {
		return "", fmt.Errorf("the gateway admin token secret %s has no %s key", secret.Name, gatewayAdminTokenKey)
	}
	return string(token), nil
}

// buildAdminNetworkPolicy admits the tool port to every caller and the admin port to the
// operator only.
func (r *MCPGatewayReconciler) buildAdminNetworkPolicy(gateway *kubemootv1alpha1.MCPGateway) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	toolPort := intstr.FromInt32(gatewayToolPort(gateway))
	adminPort := intstr.FromInt32(gatewayAdminPort(gateway))
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gateway.Name + "-admin",
			Namespace: gateway.Namespace,
			Labels:    gatewayLabels(gateway),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: gatewayLabels(gateway)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &toolPort}}},
				{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{namespaceNameLabel: operatorNamespace()}},
						PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{controlPlaneLabel: controllerManagerValue}},
					}},
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &adminPort}},
				},
			},
		},
	}
}

// reconcileAdminAccess creates the admin token Secret and the NetworkPolicy that keeps the
// admin port to the operator. It does nothing for an implementation without an admin port.
func (r *MCPGatewayReconciler) reconcileAdminAccess(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	if !gatewayHasAdminPort(gateway) {
		return nil
	}
	if err := r.ensureAdminTokenSecret(ctx, gateway); err != nil {
		return err
	}
	desired := r.buildAdminNetworkPolicy(gateway)
	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(gateway, desired, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Spec = desired.Spec
	return r.Update(ctx, existing)
}

// stampAdminTokenHash puts a fingerprint of the token on the pod template, so a Secret that
// was recreated or refilled rolls the gateway pods onto the new token.
func (r *MCPGatewayReconciler) stampAdminTokenHash(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, deployment *appsv1.Deployment) error {
	if !gatewayHasAdminPort(gateway) {
		return nil
	}
	token, err := r.kubemootAdminToken(ctx, gateway)
	if err != nil {
		return err
	}
	tmpl := &deployment.Spec.Template
	if tmpl.Annotations == nil {
		tmpl.Annotations = map[string]string{}
	}
	tmpl.Annotations[adminTokenHashAnnotation] = adminTokenHash(token)
	return nil
}
