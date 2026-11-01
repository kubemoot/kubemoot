/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// GatewayAsker asks a crew through its discussion gateway, reached through the API
// server's service proxy: the path kmctl conversation ask and the quickstart use. The
// asker therefore sees what a person sees, and "one answer delivered" is counted on the
// stream the person's client reads.
type GatewayAsker struct {
	K         Kubectl
	Namespace string
	Crew      string
}

func (g GatewayAsker) proxy() string {
	return fmt.Sprintf("/api/v1/namespaces/%s/services/%s-discussion:80/proxy/api/v1/discussions/%s", g.Namespace, g.Crew, g.Crew)
}

// Ask posts the question and starts reading the conversation's stream.
func (g GatewayAsker) Ask(ctx context.Context, question string) (Asked, error) {
	body, err := json.Marshal(map[string]string{"message": question})
	if err != nil {
		return nil, err
	}
	raw, err := g.K.Run(ctx, body, "create", "--raw", g.proxy(), "-f", "-")
	if err != nil {
		return nil, err
	}
	var resp struct {
		ConversationID string `json:"conversationId"`
		RequestedAt    string `json:"requestedAt"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.ConversationID == "" {
		return nil, fmt.Errorf("the gateway answered %q, not a conversation", strings.TrimSpace(string(raw)))
	}
	path := fmt.Sprintf("%s/%s/stream?since=%s", g.proxy(), resp.ConversationID, url.QueryEscape(resp.RequestedAt))
	return startStream(ctx, g.K, resp.ConversationID, path)
}

// stream is a conversation's server-sent events read through kubectl.
type stream struct {
	conv   string
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	count  int
	err    error
}

func startStream(ctx context.Context, k Kubectl, conv, path string) (*stream, error) {
	sctx, cancel := context.WithCancel(ctx)
	cmd := k.Command(sctx, "get", "--raw", path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	s := &stream{conv: conv, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			s.apply(sc.Text())
		}
		werr := cmd.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		if werr != nil && s.err == nil && sctx.Err() == nil {
			s.err = fmt.Errorf("the stream ended: %w", werr)
		}
	}()
	return s, nil
}

// StreamEventKind reads one line of the gateway's stream and returns the event type and
// the error text, when the line is a data line.
func StreamEventKind(line string) (kind, errText string, ok bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", "", false
	}
	var ev struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev) != nil {
		return "", "", false
	}
	return ev.Type, ev.Error, true
}

func (s *stream) apply(line string) {
	kind, errText, ok := StreamEventKind(line)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case "synthesis":
		s.count++
	case "error":
		s.err = fmt.Errorf("the gateway reported an error: %s", errText)
	}
}

func (s *stream) ConversationID() string { return s.conv }
func (s *stream) Done() <-chan struct{}  { return s.done }

func (s *stream) Syntheses() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *stream) Close() {
	s.cancel()
	<-s.done
}
