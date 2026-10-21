package a2a

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Turner builds the turn message from the contract file's identity and sends it to the harness.
type Turner struct {
	Client          *Client
	Agent, Crew     string
	Role, Phase     string
	Instructions    string
	Models          map[string]any
	DeadlineSeconds int
}

// Do implements bus.Turner.
func (t *Turner) Do(ctx context.Context, threadID, question string, thread []ThreadEntry,
	key string) (SendResponse, error) {
	msg, err := NewTurn(uuid.NewString(), threadID, question, TurnInput{
		Kind: "discussion", Agent: t.Agent, Crew: t.Crew, Role: t.Role, Phase: t.Phase,
		Instructions: t.Instructions, Thread: thread, Models: t.Models, TurnKey: key,
		DeadlineSeconds: t.DeadlineSeconds, Now: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return SendResponse{}, err
	}
	return t.Client.Send(ctx, msg)
}
