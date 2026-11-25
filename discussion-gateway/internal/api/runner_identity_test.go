package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	runnerUser     = "system:serviceaccount:ns:fitness-runner"
	runnerToken    = "runner-token"
	runnerAudience = "kubemoot-gateway"
)

// runnerCluster is a fake API server that knows the fitness runner's projected token, bound to
// runnerAudience, and grants it get on crews only when granted is true.
func runnerCluster(granted bool) *fake.Clientset {
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		ok := tr.Spec.Token == runnerToken
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: ok, User: authenticationv1.UserInfo{Username: runnerUser}, Audiences: []string{runnerAudience}}}, nil
	})
	c.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		allowed := granted && sar.Spec.User == runnerUser && sar.Spec.ResourceAttributes.Verb == "get" &&
			sar.Spec.ResourceAttributes.Resource == "crews" && sar.Spec.ResourceAttributes.Name == "pilot"
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	return c
}

// A crew with allowAnonymous false and tokenAudiences refuses the fitness runner with no token,
// refuses it without the grant on the Crew, and serves it with the projected token and the grant.
func TestACrewThatRequiresIdentityServesTheFitnessRunnerOnlyWithItsTokenAndGrant(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	serve := func(granted bool) *httptest.Server {
		rv := identity.Reviewer{Client: runnerCluster(granted), Audiences: []string{runnerAudience}}
		auth := identity.NewAuthenticator(identity.Hasher{}, false, identity.TokenReviewVerifier{Reviewer: rv})
		mux := http.NewServeMux()
		NewHandler(nc, "ns").WithAccess(auth, rv).RegisterRoutes(mux)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv
	}
	body := `{"message":"hi"}`

	if got := do(t, http.MethodPost, serve(true).URL+postPath, "", body).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", got)
	}
	if got := do(t, http.MethodPost, serve(false).URL+postPath, runnerToken, body).StatusCode; got != http.StatusForbidden {
		t.Errorf("token without the grant: status %d, want 403", got)
	}
	if got := do(t, http.MethodPost, serve(true).URL+postPath, runnerToken, body).StatusCode; got != http.StatusOK {
		t.Errorf("token with the grant: status %d, want 200", got)
	}
}
