package signin

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// protectedResourceHandler names the liaison itself as the authorization server for /mcp.
func protectedResourceHandler(publicURL string) http.Handler {
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               publicURL + "/mcp",
		AuthorizationServers:   []string{publicURL},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Kubemoot crew liaison",
	})
}

// authServerMetadata is RFC 8414 metadata. It is a local type because the SDK's
// oauthex.AuthServerMeta always writes "jwks_uri", and this server signs nothing, so an
// empty value would fail strict clients.
type authServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	ResponseTypesSupported        []string `json:"response_types_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

func (f *front) serveAuthServerMetadata(w http.ResponseWriter, _ *http.Request) {
	base := f.cfg.PublicURL
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, authServerMetadata{
		Issuer:                        base,
		AuthorizationEndpoint:         base + pathAuthorize,
		TokenEndpoint:                 base + pathToken,
		RegistrationEndpoint:          base + pathRegister,
		ResponseTypesSupported:        []string{responseCode},
		GrantTypesSupported:           []string{grantAuthCode, grantRefresh},
		CodeChallengeMethodsSupported: []string{methodS256},
		TokenEndpointAuthMethods:      []string{authMethodNone},
	})
}
