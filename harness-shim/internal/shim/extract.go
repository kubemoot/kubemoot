package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// partText is the type of a text content block.
const partText = "text"

// Extract turns the command's stdout into the answer. An empty answer is an error: the sidecar
// reports it as a failure, never as silence.
func Extract(kind, stdout string) (string, error) {
	var out string
	var err error
	switch kind {
	case OutputGooseJSON:
		out, err = extractGoose(stdout)
	default:
		out = strings.TrimSpace(stdout)
	}
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", errors.New("the command printed no answer")
	}
	return out, nil
}

type gooseOutput struct {
	Messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
	Metadata struct {
		Status string `json:"status"`
	} `json:"metadata"`
}

// extractGoose takes the text of the last assistant message from `goose run --output-format json`.
// The plain text mode prints tool-trace lines into the answer, so it is not used.
func extractGoose(stdout string) (string, error) {
	var g gooseOutput
	if err := json.Unmarshal([]byte(stdout), &g); err != nil {
		return "", fmt.Errorf("goose printed no JSON result: %w", err)
	}
	if s := g.Metadata.Status; s != "" && s != "completed" {
		return "", fmt.Errorf("goose ended with status %q", s)
	}
	for i := len(g.Messages) - 1; i >= 0; i-- {
		if g.Messages[i].Role != "assistant" {
			continue
		}
		var parts []string
		for _, c := range g.Messages[i].Content {
			if c.Type == partText && strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n")), nil
		}
	}
	return "", nil
}
