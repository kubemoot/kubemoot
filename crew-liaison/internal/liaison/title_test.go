package liaison

import (
	"net/http/httptest"
	"testing"
)

func TestDisplayNameIsTheServerTitle(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	for _, tc := range []struct{ name, set, want string }{
		{"default", "", "Kubemoot"},
		{"blank", "   ", "Kubemoot"},
		{"set", "Example Library", "Example Library"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(NewHandler(svc.WithDisplayName(tc.set), ""))
			defer srv.Close()
			resp := post(t, srv.URL+"/mcp", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"c","version":"0"}}}`)
			result, _ := resp["result"].(map[string]any)
			info, _ := result["serverInfo"].(map[string]any)
			if info["title"] != tc.want {
				t.Errorf("serverInfo %v, want title %q", info, tc.want)
			}
		})
	}
}
