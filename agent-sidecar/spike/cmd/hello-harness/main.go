// Command hello-harness is the smallest harness (conformance level 1): it serves an A2A agent card and
// SendMessage, calls the model door with the OpenAI API, and replies with text. No Kubemoot library.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9000", "listen address")
	advertise := flag.String("advertise", "", "URL the agent card gives (default http://<addr>)")
	flag.Parse()
	if *advertise == "" {
		*advertise = "http://" + *addr
	}
	modelURL := os.Getenv("KUBEMOOT_MODEL_URL")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"name":"hello-harness","capabilities":{"streaming":false},"supportedInterfaces":[{"url":%q,"protocolBinding":"HTTP+JSON","protocolVersion":"1.0"}]}`, *advertise)
	})
	mux.HandleFunc("POST /message:send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message struct {
				ContextID string `json:"contextId"`
				Parts     []struct {
					Text string          `json:"text"`
					Data json.RawMessage `json:"data"`
				} `json:"parts"`
			} `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		question, key := "", ""
		for _, p := range in.Message.Parts {
			question += p.Text
			if len(p.Data) > 0 {
				var d struct {
					TurnKey string `json:"turnKey"`
				}
				_ = json.Unmarshal(p.Data, &d)
				key = d.TurnKey
			}
		}
		answer := ask(modelURL, key, question)
		w.Header().Set("Content-Type", "application/a2a+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{
			"id": "t-" + in.Message.ContextID, "contextId": in.Message.ContextID,
			"status":    map[string]any{"state": "TASK_STATE_COMPLETED"},
			"artifacts": []any{map[string]any{"parts": []any{map[string]any{"text": answer}}}},
		}})
	})
	log.Fatal((&http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

func ask(modelURL, key, question string) string {
	body, _ := json.Marshal(map[string]any{"model": "default", "messages": []map[string]string{{"role": "user", "content": question}}})
	req, _ := http.NewRequest(http.MethodPost, modelURL+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Choices) == 0 {
		return ""
	}
	return out.Choices[0].Message.Content
}
