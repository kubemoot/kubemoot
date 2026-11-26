package main

import (
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const (
	brokerSubject    = "system:serviceaccount:ns:gw"
	liaisonBrokerURL = "http://liaison:8081"
	pilotCrew        = "pilot"
)

const twoServices = `[{"name":"oreilly","verifyURL":"https://a.example/mcp","subjectClaim":"sub","audiences":["a"],"passToTools":true},` +
	`{"name":"plain","verifyURL":"https://b.example/mcp","subjectClaim":"sub","audiences":["b"]}]`

func TestOnlyVerifiersThatPassTokensToToolsKeepThem(t *testing.T) {
	c, err := credentialConfigFromEnv(envOf(map[string]string{
		identity.EnvServiceTokens: twoServices, envBrokerSubject: brokerSubject, envMaxGrants: "7",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.issuers) != 1 || c.issuers[0] != "oreilly" || !c.keepsTokens() || !c.leases() {
		t.Fatalf("%+v", c)
	}
	if c.internalPort != "8081" || c.maxGrants != 7 {
		t.Fatalf("port %q max %d", c.internalPort, c.maxGrants)
	}
}

func TestNothingIsKeptWithoutAPassingVerifier(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no service tokens":     {envBrokerSubject: brokerSubject},
		"no passToTools":        {identity.EnvServiceTokens: strings.Replace(twoServices, `"passToTools":true`, `"passToTools":false`, 1)},
		"unusable bound":        {identity.EnvServiceTokens: twoServices, envMaxGrants: "lots"},
		"a lease subject alone": {envBrokerSubject: brokerSubject},
	} {
		c, err := credentialConfigFromEnv(envOf(env))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "unusable bound" {
			if c.maxGrants != 0 || !c.keepsTokens() {
				t.Errorf("%s: %+v, want the default bound", name, c)
			}
			continue
		}
		if c.keepsTokens() || c.leases() {
			t.Errorf("%s: %+v keeps tokens", name, c)
		}
	}
}

func TestTheLeaseListenerNeedsTheNamedServiceAccount(t *testing.T) {
	c, err := credentialConfigFromEnv(envOf(map[string]string{identity.EnvServiceTokens: twoServices, envInternalPort: "9090"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.keepsTokens() || c.leases() || c.internalPort != "9090" {
		t.Fatalf("%+v: tokens are kept and no listener runs without %s", c, envBrokerSubject)
	}
}

func TestAnInvalidServiceTokenListStopsTheSetup(t *testing.T) {
	if _, err := credentialConfigFromEnv(envOf(map[string]string{identity.EnvServiceTokens: `not json`})); err == nil {
		t.Fatal("an invalid list was accepted")
	}
}

func TestTheLeaseListenerRunsForConnectedAccountsWithNoKeptTokens(t *testing.T) {
	c, err := credentialConfigFromEnv(envOf(map[string]string{
		envBrokerSubject: brokerSubject, identity.EnvAccountCrew: pilotCrew, identity.EnvAccountBrokerURL: liaisonBrokerURL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.keepsTokens() || !c.leasesAccounts() || !c.leases() || c.crew != pilotCrew {
		t.Fatalf("%+v", c)
	}
}

func TestConnectedAccountLeasesNeedTheCrewTheBrokerAndTheNamedServiceAccount(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no crew":            {envBrokerSubject: brokerSubject, identity.EnvAccountBrokerURL: liaisonBrokerURL},
		"no broker":          {envBrokerSubject: brokerSubject, identity.EnvAccountCrew: pilotCrew},
		"no ServiceAccount":  {identity.EnvAccountCrew: pilotCrew, identity.EnvAccountBrokerURL: liaisonBrokerURL},
		"nothing configured": {},
	} {
		c, err := credentialConfigFromEnv(envOf(env))
		if err != nil || c.leases() {
			t.Errorf("%s: %+v %v", name, c, err)
		}
	}
}
