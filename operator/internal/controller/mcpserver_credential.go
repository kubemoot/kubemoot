package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// Secret keys an MCPServer's registry.authSecretRef may carry.
	secretKeyBearerToken = "bearer-token"
	secretKeyAPIKey      = "api-key"

	// defaultAPIKeyHeader carries an api-key when registry.authHeader is unset.
	defaultAPIKeyHeader = "X-API-Key"
	headerAuthorization = "Authorization"

	// conditionTypeCredentialReady reports whether the credential an external MCP
	// server needs was read from its Secret.
	conditionTypeCredentialReady = "CredentialReady"

	reasonCredentialFound      = "CredentialFound"
	reasonCredentialNoSecret   = "SecretNotFound"
	reasonCredentialNoKey      = "KeyMissing"
	reasonCredentialBadHeader  = "InvalidHeader"
	reasonCredentialReadFailed = "SecretUnreadable"

	redactedValue = "<redacted>"
)

// registryCredential is a header the gateway sends on every call to one MCP server.
// The value is a secret: it is held only for the length of a registration call, and
// String and GoString keep it out of any log line or error that formats the struct.
type registryCredential struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

func (c registryCredential) String() string {
	return fmt.Sprintf("registryCredential{header=%s, value=%s}", c.Header, redactedValue)
}

// GoString keeps %#v from printing the value.
func (c registryCredential) GoString() string { return c.String() }

// credentialError says why a credential could not be read. Its message names the Secret
// and the key, never a value.
type credentialError struct {
	Reason  string
	Message string
}

func (e *credentialError) Error() string { return e.Message }

// credentialReason is the condition reason for err: the credentialError's own, or a
// generic one for anything else.
func credentialReason(err error) string {
	var ce *credentialError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return reasonCredentialReadFailed
}

// registryAuthSecretName is the Secret the MCPServer keeps its credential in, or "".
func registryAuthSecretName(mcp *kubemootv1alpha1.MCPServer) string {
	if mcp.Spec.Registry == nil {
		return ""
	}
	return mcp.Spec.Registry.AuthSecretRef
}

// resolveRegistryCredential reads the credential an MCPServer's registry.authSecretRef
// names: bearer-token becomes "Authorization: Bearer <token>", api-key becomes the header
// named by registry.authHeader (X-API-Key when unset) with the key as its value. It
// returns nil and no error when the server has no authSecretRef.
func resolveRegistryCredential(ctx context.Context, c client.Reader, mcp *kubemootv1alpha1.MCPServer) (*registryCredential, error) {
	name := registryAuthSecretName(mcp)
	if name == "" {
		return nil, nil
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: mcp.Namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &credentialError{Reason: reasonCredentialNoSecret,
				Message: fmt.Sprintf("credential Secret %q not found", name)}
		}
		logf.FromContext(ctx).V(1).Info("Could not read a credential Secret", "secret", name, "errorType", fmt.Sprintf("%T", err))
		return nil, &credentialError{Reason: reasonCredentialReadFailed,
			Message: fmt.Sprintf("credential Secret %q could not be read", name)}
	}
	return credentialFromSecret(name, mcp.Spec.Registry.AuthHeader, secret.Data)
}

// credentialFromSecret picks the header and value out of the Secret data.
func credentialFromSecret(name, authHeader string, data map[string][]byte) (*registryCredential, error) {
	var cred *registryCredential
	if token := strings.TrimSpace(string(data[secretKeyBearerToken])); token != "" {
		cred = &registryCredential{Header: headerAuthorization, Value: "Bearer " + token}
	} else if key := strings.TrimSpace(string(data[secretKeyAPIKey])); key != "" {
		header := authHeader
		if header == "" {
			header = defaultAPIKeyHeader
		}
		cred = &registryCredential{Header: header, Value: key}
	}
	if cred == nil {
		return nil, &credentialError{Reason: reasonCredentialNoKey,
			Message: fmt.Sprintf("credential Secret %q has neither a %q nor an %q key (or it is empty)",
				name, secretKeyBearerToken, secretKeyAPIKey)}
	}
	if !validHeaderName(cred.Header) {
		return nil, &credentialError{Reason: reasonCredentialBadHeader,
			Message: fmt.Sprintf("registry.authHeader %q is not a valid HTTP header name", cred.Header)}
	}
	if strings.ContainsAny(cred.Value, "\r\n") {
		return nil, &credentialError{Reason: reasonCredentialBadHeader,
			Message: fmt.Sprintf("the value in credential Secret %q spans more than one line", name)}
	}
	return cred, nil
}

// validHeaderName reports whether s is a non-empty RFC 7230 token.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isTokenRune(r) {
			return false
		}
	}
	return true
}

func isTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// attachRegistrationCredential adds the credential to a gateway registration: for the
// Kubemoot gateway as {"auth": {"header": ..., "value": ...}}, for ContextForge as its
// bearer auth_type and auth_token (it takes a bearer token only). It returns the credential
// so the caller can scrub the value from anything it reports.
func (r *MCPGatewayReconciler) attachRegistrationCredential(ctx context.Context, impl kubemootv1alpha1.MCPGatewayImplementation,
	mcp *kubemootv1alpha1.MCPServer, payload map[string]interface{}) (*registryCredential, error) {
	cred, err := resolveRegistryCredential(ctx, r.Client, mcp)
	if err != nil || cred == nil {
		return nil, err
	}
	if impl != kubemootv1alpha1.ImplementationContextForge {
		payload["auth"] = cred
		return cred, nil
	}
	if token, ok := strings.CutPrefix(cred.Value, "Bearer "); ok && cred.Header == headerAuthorization {
		payload["auth_type"] = "bearer"
		payload["auth_token"] = token
	}
	return cred, nil
}

// redact removes the credential's value from text that may echo a request.
func (c *registryCredential) redact(text string) string {
	if c == nil || c.Value == "" {
		return text
	}
	text = strings.ReplaceAll(text, c.Value, redactedValue)
	return strings.ReplaceAll(text, strings.TrimPrefix(c.Value, "Bearer "), redactedValue)
}

// secretToMCPServers lists the MCPServers (in its namespace) that name
// it as their registry.authSecretRef, so a rotated key reaches their status and the gateway.
// The Secret's data is never read here.
func secretToMCPServers(ctx context.Context, c client.Reader, secret client.Object) []kubemootv1alpha1.MCPServer {
	list := &kubemootv1alpha1.MCPServerList{}
	if err := c.List(ctx, list, client.InNamespace(secret.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Could not list MCPServers for a changed Secret", "secret", secret.GetName())
		return nil
	}
	var matched []kubemootv1alpha1.MCPServer
	for i := range list.Items {
		if registryAuthSecretName(&list.Items[i]) == secret.GetName() {
			matched = append(matched, list.Items[i])
		}
	}
	return matched
}

// checkCredential records on the MCPServer whether the credential it needs can be read,
// as the CredentialReady condition (removed when the server needs none). The error is
// the reason, safe to show: it names the Secret and key, never a value.
func (r *MCPServerReconciler) checkCredential(ctx context.Context, mcp *kubemootv1alpha1.MCPServer) error {
	if registryAuthSecretName(mcp) == "" {
		meta.RemoveStatusCondition(&mcp.Status.Conditions, conditionTypeCredentialReady)
		return nil
	}
	_, err := resolveRegistryCredential(ctx, r.Client, mcp)
	condition := metav1.Condition{
		Type:    conditionTypeCredentialReady,
		Status:  metav1.ConditionTrue,
		Reason:  reasonCredentialFound,
		Message: fmt.Sprintf("credential read from Secret %q", registryAuthSecretName(mcp)),
	}
	if err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = credentialReason(err)
		condition.Message = err.Error()
	}
	meta.SetStatusCondition(&mcp.Status.Conditions, condition)
	return err
}

// findMCPServersForSecret is a reconcile request for each MCPServer that takes its
// credential from the Secret.
func (r *MCPServerReconciler) findMCPServersForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	servers := secretToMCPServers(ctx, r.Client, obj)
	requests := make([]reconcile.Request, 0, len(servers))
	for _, mcp := range servers {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&mcp)})
	}
	return requests
}
