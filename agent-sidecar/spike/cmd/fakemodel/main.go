// Command fakemodel is a stand-in OpenAI-compatible server for the spike: instant answers, optional
// streaming with usage, no model.
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
	addr := flag.String("addr", "127.0.0.1:9101", "listen address")
	name := flag.String("name", "fake", "server name echoed in replies")
	tokenGap := flag.Duration("token-gap", 0, "delay between streamed tokens")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			stream(w, *name, req.Model, *tokenGap)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok from %s"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`, req.Model, *name)
	})
	log.Fatal((&http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

func stream(w http.ResponseWriter, name, model string, gap time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	rc := http.NewResponseController(w)
	for i := 0; i < 5; i++ {
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"c1\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tok%d \"}}]}\n\n", model, i)
		_ = rc.Flush()
		time.Sleep(gap)
	}
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5,\"total_tokens\":17}}\n\ndata: [DONE]\n\n")
	_ = rc.Flush()
	_ = name
}
