package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvHelpers(t *testing.T) {
	t.Setenv("LIAISON_TEST_STR", "x")
	t.Setenv("LIAISON_TEST_INT", "7")
	t.Setenv("LIAISON_TEST_BAD", "no")
	t.Setenv("LIAISON_TEST_ZERO", "0")
	if envOr("LIAISON_TEST_STR", "d") != "x" || envOr("LIAISON_TEST_MISSING", "d") != "d" {
		t.Fatal("envOr")
	}
	if envInt("LIAISON_TEST_INT", 1) != 7 || envInt("LIAISON_TEST_BAD", 1) != 1 || envInt("LIAISON_TEST_ZERO", 1) != 1 || envInt("LIAISON_TEST_MISSING", 3) != 3 {
		t.Fatal("envInt")
	}
}

const (
	httpsReportURL = "https://op-report.ns.svc:8082"
	plainReportURL = "http://op-report.ns.svc:8082"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestReportExporterConfiguration(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, testCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		env     map[string]string
		none    bool
		wantErr string
	}{
		{"nothing set", map[string]string{}, true, ""},
		{"a plain http URL is refused", map[string]string{envReportURL: plainReportURL}, false, "not https"},
		{"a plain http URL with the opt-in", map[string]string{envReportURL: plainReportURL, envAllowInsecure: trueValue}, false, ""},
		{"https with the CA file", map[string]string{envReportURL: httpsReportURL, envCAFile: ca}, false, ""},
		{"https with no CA file uses the system roots", map[string]string{envReportURL: httpsReportURL}, false, ""},
		{"https with an unreadable CA file", map[string]string{envReportURL: httpsReportURL, envCAFile: "/nonexistent/ca.crt"}, false, "CA file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := reportExporter(envMap(c.env))
			switch {
			case c.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want an error containing %q, got %v", c.wantErr, err)
				}
			case err != nil:
				t.Fatal(err)
			case c.none != (e == nil):
				t.Fatalf("exporter %v, want none=%v", e, c.none)
			}
		})
	}
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCrewGatewayIsHTTPSUnlessPlainHTTPIsAccepted(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, testCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if g, err := crewGateway(envMap(map[string]string{envCAFile: ca})); err != nil || g == nil {
		t.Fatalf("with a CA file: %v", err)
	}
	if _, err := crewGateway(envMap(nil)); err == nil || !strings.Contains(err.Error(), envCAFile) {
		t.Fatalf("with neither a CA file nor the opt-in: %v", err)
	}
	if _, err := crewGateway(envMap(map[string]string{envCAFile: "/nonexistent/ca.crt"})); err == nil {
		t.Fatal("an unreadable CA file was accepted")
	}
	if g, err := crewGateway(envMap(map[string]string{envAllowInsecureGateway: trueValue})); err != nil || g == nil {
		t.Fatalf("with the opt-in: %v", err)
	}
}
