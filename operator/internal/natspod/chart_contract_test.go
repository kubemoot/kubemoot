package natspod

import (
	"os"
	"strings"
	"testing"
)

// The charts mount the same Secrets at the same paths under the same names as this package, so a
// pod the chart builds and a pod the operator builds read the options alike. A chart helper that
// drifts from these constants fails here.
func TestChartHelpersUseTheSameNamesAndPathsAsThePackage(t *testing.T) {
	shared := []string{
		dirCA, dirClient, dirAuth, volumeCA, volumeClient, volumeAuth,
		dirCA + "/" + defaultCAKey, dirClient + "/" + clientCertKey, dirClient + "/" + clientKeyKey,
		dirAuth + "/" + credsFileName, "path: " + credsFileName, "path: " + defaultCAKey,
		"NATS_CA_FILE", "NATS_CLIENT_CERT_FILE", "NATS_CLIENT_KEY_FILE", "NATS_CREDS_FILE",
		"NATS_USER", "NATS_PASSWORD", "NATS_TOKEN",
	}
	charts := map[string][]string{
		"../../chart/kubemoot-operator/templates/_helpers.tpl": append([]string{
			EnvCASecret, EnvCAKey, EnvClientSecret, EnvAuthSecret, EnvAuthCredsKey, EnvAuthUserKey,
			EnvAuthPasswdKey, EnvAuthTokenKey,
		}, shared...),
		"../../../dashboard/charts/kubemoot-dashboard/templates/_helpers.tpl": shared,
	}
	for file, want := range charts {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s does not contain %q", file, w)
			}
		}
	}
}
