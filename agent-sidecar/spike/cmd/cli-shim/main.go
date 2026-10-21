// Command cli-shim is the harness-agnostic shim of the design: it serves the A2A agent card and
// SendMessage and runs a command once per turn, with the model URL, tools URL and per-turn key in the
// command's environment, mapping stdout to the reply. The command template is configuration, so one shim
// serves goose, claude -p, or any command-line agent.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, " ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type shim struct {
	argv, env []string
	modelURL  string
	toolsURL  string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9400", "listen address")
	advertise := flag.String("advertise", "", "URL the agent card gives (default http://<addr>)")
	var argv, env listFlag
	flag.Var(&argv, "arg", "command argument; repeatable; {{question}} is replaced")
	flag.Var(&env, "env", "KEY=VALUE for the command; repeatable; {{modelURL}} {{toolsURL}} {{turnKey}} are replaced")
	flag.Parse()
	if *advertise == "" {
		*advertise = "http://" + *addr
	}
	s := &shim{argv: argv, env: env, modelURL: os.Getenv("KUBEMOOT_MODEL_URL"), toolsURL: os.Getenv("KUBEMOOT_TOOLS_URL")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"name":"cli-shim","capabilities":{"streaming":false},"supportedInterfaces":[{"url":%q,"protocolBinding":"HTTP+JSON","protocolVersion":"1.0"}]}`, *advertise)
	})
	mux.HandleFunc("POST /message:send", s.send)
	log.Fatal((&http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

type turnIn struct {
	Message struct {
		ContextID string `json:"contextId"`
		Parts     []struct {
			Text string          `json:"text"`
			Data json.RawMessage `json:"data"`
		} `json:"parts"`
	} `json:"message"`
}

func (s *shim) send(w http.ResponseWriter, r *http.Request) {
	var in turnIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	question, key, deadline := "", "", 300
	for _, p := range in.Message.Parts {
		question += p.Text
		if len(p.Data) > 0 {
			var d struct {
				TurnKey         string `json:"turnKey"`
				DeadlineSeconds int    `json:"deadlineSeconds"`
			}
			_ = json.Unmarshal(p.Data, &d)
			key = d.TurnKey
			if d.DeadlineSeconds > 0 {
				deadline = d.DeadlineSeconds
			}
		}
	}
	out, err := s.run(r.Context(), question, key, time.Duration(deadline)*time.Second)
	state, part := "TASK_STATE_COMPLETED", out
	if err != nil {
		state, part = "TASK_STATE_FAILED", err.Error()
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{
		"id": "t-" + in.Message.ContextID, "contextId": in.Message.ContextID,
		"status":    map[string]any{"state": state, "message": map[string]any{"role": "ROLE_AGENT", "parts": []any{map[string]any{"text": part}}}},
		"artifacts": artifacts(state, part),
	}})
}

func artifacts(state, text string) []any {
	if state != "TASK_STATE_COMPLETED" {
		return nil
	}
	return []any{map[string]any{"parts": []any{map[string]any{"text": text}}}}
}

func (s *shim) run(parent context.Context, question, key string, limit time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	repl := strings.NewReplacer("{{question}}", question, "{{modelURL}}", s.modelURL, "{{toolsURL}}", s.toolsURL,
		"{{turnKey}}", key, "{{modelHost}}", strings.TrimSuffix(s.modelURL, "/v1"))
	args := make([]string, len(s.argv))
	for i, a := range s.argv {
		args[i] = repl.Replace(a)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = os.Environ()
	for _, e := range s.env {
		cmd.Env = append(cmd.Env, repl.Replace(e))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
