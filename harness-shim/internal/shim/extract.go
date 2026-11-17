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
			Type    string `json:"type"`
			Text    string `json:"text"`
			Message string `json:"message"`
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
	if err := json.NewDecoder(strings.NewReader(jsonResult(stdout))).Decode(&g); err != nil {
		return "", fmt.Errorf("goose printed no JSON result: %w", err)
	}
	if err := g.endedWell(); err != nil {
		return "", err
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

// jsonResult is stdout from the line where goose's JSON object starts. goose 1.55.0 prints a banner
// (an ASCII goose and "goose is ready") to stdout before the result unless it runs quiet, and the
// result is the one object that starts at the beginning of a line.
func jsonResult(stdout string) string {
	if strings.HasPrefix(stdout, "{") {
		return stdout
	}
	if i := strings.Index(stdout, "\n{"); i >= 0 {
		return stdout[i+1:]
	}
	return stdout
}

// lastError is the message of the newest error block in the transcript, which says why a run ended.
func (g gooseOutput) lastError() string {
	for i := len(g.Messages) - 1; i >= 0; i-- {
		for _, c := range g.Messages[i].Content {
			if c.Type == "error" && strings.TrimSpace(c.Message) != "" {
				return strings.Join(strings.Fields(c.Message), " ")
			}
		}
	}
	return ""
}

// endedWell is nil when the run completed, else an error with the status and the reason goose gave.
func (g gooseOutput) endedWell() error {
	s := g.Metadata.Status
	if s == "" || s == "completed" {
		return nil
	}
	if said := g.lastError(); said != "" {
		return fmt.Errorf("goose ended with status %q: %s", s, said)
	}
	return fmt.Errorf("goose ended with status %q", s)
}
