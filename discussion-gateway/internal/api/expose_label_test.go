package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// labelServer is an enforcing gateway that tells the crew's agents who is asking, or not.
func labelServer(t *testing.T, expose bool) (*httptest.Server, ownedSetup) {
	t.Helper()
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	rv := identity.Reviewer{Client: cluster()}
	auth := identity.NewAuthenticator(identity.Hasher{}, true, identity.TokenReviewVerifier{Reviewer: rv})
	mux := http.NewServeMux()
	NewHandler(nc, "ns").WithAccess(auth, rv).ExposeLabel(expose).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	kv, err := js.KeyValue(context.Background(), bus.ConversationsBucket)
	if err != nil {
		t.Fatal(err)
	}
	return srv, ownedSetup{srv: srv, js: js, kv: kv}
}

func TestNoLabelIsStampedUnlessTheCrewExposesTheCaller(t *testing.T) {
	srv, o := labelServer(t, false)
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenAlice, `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	p := queued(t, o.js).Principal
	if p == nil || p.DisplayName != "" || p.ID == "" {
		t.Fatalf("principal %+v: want the id and no label", p)
	}
}

func TestAKubernetesCallersUserNameIsTheLabelWhenTheCrewExposesTheCaller(t *testing.T) {
	srv, o := labelServer(t, true)
	if resp := do(t, http.MethodPost, srv.URL+postPath, tokenAlice, `{"message":"hi","conversationId":"c1"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	p := queued(t, o.js).Principal
	if p == nil || p.DisplayName != userAlice || p.ID != (identity.Hasher{}).ID(identity.IssuerKubernetes, userAlice) {
		t.Fatalf("principal %+v: want id and label %q", p, userAlice)
	}
	entry, err := o.kv.Get(context.Background(), bus.ConversationOwnerKey("ns", "pilot", "c1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(entry.Value()), userAlice) {
		t.Fatalf("the owner record holds the name: %s", entry.Value())
	}
}

func TestAnAnonymousCallerHasNoLabelEvenWhenTheCrewExposesTheCaller(t *testing.T) {
	srv, o := labelServer(t, true)
	if resp := do(t, http.MethodPost, srv.URL+postPath, "", `{"message":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if p := queued(t, o.js).Principal; p == nil || p.Kind != bus.PrincipalKindAnonymous || p.DisplayName != "" {
		t.Fatalf("principal %+v: want anonymous with no label", p)
	}
}

func TestOnlyAPlainTokenIsStampedAsTheLabel(t *testing.T) {
	cases := map[string]string{
		"alice":                            "alice",
		"alice@example.org":                "alice@example.org",
		"system:serviceaccount:ns:builder": "system:serviceaccount:ns:builder",
		"Alice Smith":                      "",
		`a"b`:                              "",
		"a)b(c":                            "",
		"line\nbreak":                      "",
		"":                                 "",
		strings.Repeat("x", 200):           strings.Repeat("x", maxLabelChars),
		strings.Repeat("x", 63) + " tail":  "",
	}
	for subject, want := range cases {
		if got := labelOf(subject); got != want {
			t.Errorf("labelOf(%q) = %q, want %q", subject, got, want)
		}
	}
}
