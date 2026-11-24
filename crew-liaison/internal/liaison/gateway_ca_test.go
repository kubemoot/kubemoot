package liaison

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheGatewayClientUsesHTTPSAtTheCrewServiceAndTrustsOnlyTheCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	g, err := NewGatewayWithCA(writeCA(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if got := g.baseURL("crew-ns", "pilot"); got != "https://pilot-discussion.crew-ns.svc/api/v1/discussions/pilot" {
		t.Fatalf("base URL %q", got)
	}
	if code, err := getVia(t, g.client.Transport, srv.URL); err != nil || code != http.StatusNotFound {
		t.Fatalf("a gateway signed by the trusted CA: %d %v", code, err)
	}
	other, err := NewGatewayWithCA(unrelatedCA(t))
	if err != nil {
		t.Fatal(err)
	}
	if code, err := getVia(t, other.client.Transport, srv.URL); err == nil {
		t.Fatalf("an untrusted gateway certificate was accepted (status %d)", code)
	}
}

func TestTheGatewayClientRefusesAnUnusableCAFile(t *testing.T) {
	if _, err := NewGatewayWithCA("/nonexistent/ca.crt"); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
}
