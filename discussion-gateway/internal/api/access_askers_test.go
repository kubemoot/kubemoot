package api

import (
	"net/http"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"k8s.io/client-go/kubernetes/fake"
)

// noKubernetes is an API server that must not be asked about the asker at all: any call fails
// the test.
func noKubernetes(t *testing.T) *fake.Clientset {
	t.Helper()
	return fake.NewSimpleClientset()
}

func assertNoKubernetesCalls(t *testing.T, c *fake.Clientset) {
	t.Helper()
	if n := len(c.Actions()); n != 0 {
		t.Errorf("%d Kubernetes API calls were made for an asker that is not a Kubernetes user: %v", n, c.Actions())
	}
}

func TestAnySignedInAdmitsAnyAssertedPersonWithoutAskingKubernetes(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	k := noKubernetes(t)
	h := newAskersGateway(t, nc, k, false, `{"anySignedIn":{}}`)
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, "someone@example.org"), `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	p := queued(t, js).Principal
	if p == nil || p.Kind != bus.PrincipalKindSubscriber || p.Issuer != identity.HopIssuerPrefix+hopIssuer || p.ID == "" {
		t.Fatalf("principal %+v", p)
	}
	assertNoKubernetesCalls(t, k)
}

func TestIssuerGroupsAdmitAMemberAndRefuseOthersWithoutAskingKubernetes(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	k := noKubernetes(t)
	h := newAskersGateway(t, nc, k, false, `{"issuerGroups":["`+hopTeam+`"]}`)
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJoe, hopTeam), `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a member: %d", resp.StatusCode)
	}
	for name, groups := range map[string][]string{"another group": {"oidc:other"}, "no group": nil} {
		if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJoe, groups...), `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, resp.StatusCode)
		}
	}
	assertNoKubernetesCalls(t, k)
}

func TestConnectedAccountAdmitsASignedInPerson(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	k := noKubernetes(t)
	h := newAskersGateway(t, nc, k, false, `{"connectedAccount":"library"}`)
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJane), `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	assertNoKubernetesCalls(t, k)
}

func TestAMissingOrForeignCredentialIsRefusedUnderEveryNonRBACKind(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	for _, rule := range []string{`{"anySignedIn":{}}`, `{"connectedAccount":"library"}`, `{"issuerGroups":["g"]}`} {
		h := newAskersGateway(t, nc, noKubernetes(t), false, rule)
		if resp := do(t, http.MethodPost, h.srv.URL+postPath, "", `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with no assertion: %d, want 401", rule, resp.StatusCode)
		}
		if resp := do(t, http.MethodPost, h.srv.URL+postPath, "not-an-assertion", `{"message":"hi"}`); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with a stranger's token: %d, want 401", rule, resp.StatusCode)
		}
	}
}

func TestANonRBACPersonCannotReadAnothersConversationEvenWithAReason(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	h := newAskersGateway(t, nc, noKubernetes(t), false, `{"anySignedIn":{}}`)
	start := `{"message":"hi","conversationId":"c9"}`
	if resp := do(t, http.MethodPost, h.srv.URL+postPath, h.assert(t, hopJane), start); resp.StatusCode != http.StatusOK {
		t.Fatalf("jane starts: %d", resp.StatusCode)
	}
	stream := h.srv.URL + listPath + "/c9/stream"
	if resp := do(t, http.MethodGet, stream, h.assert(t, hopJoe), "", reasonHeader, "curious"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("joe: %d, want 404", resp.StatusCode)
	}
}

func TestAPersonAdmittedByThisCrewsRuleCannotUseAnotherCrewThroughItsGateway(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	h := newAskersGateway(t, nc, noKubernetes(t), false, `{"anySignedIn":{}}`)
	if resp := do(t, http.MethodPost, h.srv.URL+"/api/v1/discussions/other", h.assert(t, hopJane), `{"message":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another crew's path: %d, want 403", resp.StatusCode)
	}
}
