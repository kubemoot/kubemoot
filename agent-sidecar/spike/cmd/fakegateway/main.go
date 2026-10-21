// Command fakegateway imitates the crew MCP gateway's REST routes (GET /tools, POST /tools/{name}/call)
// with the shapes mcp-gateway's ToolController returns today.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9200", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 3, "tools": []map[string]any{
			tool("time_get_current_time", "Current time"),
			tool("fetch", "Fetch a URL"),
			tool("shell_exec", "Run a shell command"),
		}})
	})
	mux.HandleFunc("POST /tools/{name}/call", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"isError": false,
			"content": fmt.Sprintf("result of %s at %s", r.PathValue("name"), time.Now().UTC().Format(time.RFC3339))})
	})
	log.Fatal((&http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

func tool(name, desc string) map[string]any {
	return map[string]any{"name": name, "description": desc, "serverId": "s1", "serverName": "s1",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}
}
