package liaison

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("liaison")

// Waits are capped below the tool-call timeout of common MCP clients (Claude Code
// times a call out at sixty seconds by default), so a call always returns its ticket.
// The follower's own deadline is generous: discussions are minutes long on real
// hardware.
const (
	maxWait        = 45 * time.Second
	defaultWait    = maxWait
	followDeadline = 30 * time.Minute
)

// The hint every pending result carries: the YDY ("Ya Done Yet?") pattern in one line.
const hintPending = "The crew is still deliberating; discussions take one to several minutes. " +
	"Call get_answer with this ticket; each call waits up to 45 seconds " +
	"and returns the answer as soon as it exists. " +
	"Keep calling until status is \"answered\" or \"failed\". " +
	"Do not ask again: a repeated question rejoins this same ticket."

// Service implements the liaison's tools over a crew lister, a gateway client, and a
// ticket store. It fronts every crew the lister returns.
type Service struct {
	crews    Lister
	gateway  *Gateway
	tickets  *Store
	inflight chan struct{}
	now      func() time.Time
}

// NewService wires the tools. maxInflight bounds concurrent discussions, because every
// question spends GPU time.
func NewService(crews Lister, gateway *Gateway, tickets *Store, maxInflight int) *Service {
	if maxInflight < 1 {
		maxInflight = 1
	}
	return &Service{
		crews:    crews,
		gateway:  gateway,
		tickets:  tickets,
		inflight: make(chan struct{}, maxInflight),
		now:      time.Now,
	}
}

// ListCrewsInput has no fields; the tool takes no arguments.
type ListCrewsInput struct{}

// ListCrewsOutput names the crews a client may ask.
type ListCrewsOutput struct {
	Crews []Crew `json:"crews"`
}

// Names of the input properties whose descriptions inputSchema sets.
const (
	argNamespace   = "namespace"
	argWaitSeconds = "waitSeconds"
)

// Argument descriptions too long for a struct tag line; inputSchema sets them on
// the emitted schema.
const (
	namespaceDesc = "namespace of the crew, needed only when the name is not unique"
	askWaitDesc   = "seconds to wait for the answer before returning the ticket; default and maximum 45, " +
		"0 returns the ticket at once"
	getAnswerWaitDesc = "seconds to wait for the answer before reporting pending; default and maximum 45, " +
		"0 reports the current state at once"
)

// AskInput is a question for one crew. Namespace and WaitSeconds take their
// descriptions from askInputSchema.
type AskInput struct {
	Crew        string `json:"crew" jsonschema:"name of the crew to ask; see list_crews"`
	Namespace   string `json:"namespace,omitempty"`
	Question    string `json:"question" jsonschema:"the question, in full; the crew has no memory of earlier questions"`
	WaitSeconds *int   `json:"waitSeconds,omitempty"`
}

// AskOutput is a ticket, and the answer when it arrived within the wait.
type AskOutput struct {
	TicketView
	Hint string `json:"hint,omitempty"`
}

// GetAnswerInput names a ticket from ask. WaitSeconds takes its description from
// getAnswerInputSchema.
type GetAnswerInput struct {
	Ticket      string `json:"ticket" jsonschema:"the ticket returned by ask"`
	WaitSeconds *int   `json:"waitSeconds,omitempty"`
}

// askInputSchema is the ask tool's input schema.
func askInputSchema() *jsonschema.Schema {
	return inputSchema[AskInput](map[string]string{argNamespace: namespaceDesc, argWaitSeconds: askWaitDesc})
}

// getAnswerInputSchema is the get_answer tool's input schema.
func getAnswerInputSchema() *jsonschema.Schema {
	return inputSchema[GetAnswerInput](map[string]string{argWaitSeconds: getAnswerWaitDesc})
}

// inputSchema infers T's JSON schema and sets the description of each named
// property. It panics on a type the schema cannot be inferred for, or on a name
// that is not a property, both programming errors caught at server start.
func inputSchema[T any](descriptions map[string]string) *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("input schema: %v", err))
	}
	for name, description := range descriptions {
		prop, ok := schema.Properties[name]
		if !ok {
			panic(fmt.Sprintf("input schema: no property %q", name))
		}
		prop.Description = description
	}
	return schema
}

// GetAnswerOutput is the ticket's current state, with the answer once it exists.
type GetAnswerOutput struct {
	TicketView
	Hint string `json:"hint,omitempty"`
}

// ListCrews returns the crews with a discussion gateway.
func (s *Service) ListCrews(
	ctx context.Context, _ *mcp.CallToolRequest, _ ListCrewsInput,
) (*mcp.CallToolResult, ListCrewsOutput, error) {
	crews, err := s.crews.List(ctx)
	if err != nil {
		return toolError(err.Error()), ListCrewsOutput{}, nil
	}
	return nil, ListCrewsOutput{Crews: crews}, nil
}

// Ask starts a discussion (or rejoins a pending one for the same question) and waits
// up to the requested time for the answer before returning the ticket.
func (s *Service) Ask(
	ctx context.Context, _ *mcp.CallToolRequest, in AskInput,
) (*mcp.CallToolResult, AskOutput, error) {
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return toolError("question is required"), AskOutput{}, nil
	}
	crews, err := s.crews.List(ctx)
	if err != nil {
		return toolError(err.Error()), AskOutput{}, nil
	}
	crew, err := find(crews, in.Crew, in.Namespace)
	if err != nil {
		return toolError(err.Error()), AskOutput{}, nil
	}
	ticket, err := s.startOrRejoin(ctx, crew, question)
	if err != nil {
		return toolError(err.Error()), AskOutput{}, nil
	}
	s.wait(ctx, ticket, waitFor(in.WaitSeconds))
	return nil, AskOutput{TicketView: ticket.Snapshot(), Hint: hintFor(ticket)}, nil
}

// GetAnswer reports a ticket's state, waiting up to the requested time for it to
// settle: the YDY call, as a long poll.
func (s *Service) GetAnswer(
	ctx context.Context, _ *mcp.CallToolRequest, in GetAnswerInput,
) (*mcp.CallToolResult, GetAnswerOutput, error) {
	ticket, ok := s.tickets.Get(strings.TrimSpace(in.Ticket))
	if !ok {
		return toolError("unknown or expired ticket; ask again"), GetAnswerOutput{}, nil
	}
	s.wait(ctx, ticket, waitFor(in.WaitSeconds))
	return nil, GetAnswerOutput{TicketView: ticket.Snapshot(), Hint: hintFor(ticket)}, nil
}

// startOrRejoin reuses a pending ticket for the same question, else starts a discussion.
func (s *Service) startOrRejoin(ctx context.Context, crew Crew, question string) (*Ticket, error) {
	if ticket, ok := s.tickets.FindPending(crew.Name, crew.Namespace, question); ok {
		return ticket, nil
	}
	return s.start(ctx, crew, question)
}

// start takes an in-flight slot, opens the discussion, and follows it in the background.
func (s *Service) start(ctx context.Context, crew Crew, question string) (*Ticket, error) {
	select {
	case s.inflight <- struct{}{}:
	default:
		return nil, fmt.Errorf(
			"the liaison is at its limit of %d discussions in flight; try again in a minute", cap(s.inflight))
	}
	conversation, err := s.gateway.Start(ctx, crew.Namespace, crew.Name, question)
	if err != nil {
		<-s.inflight
		return nil, err
	}
	ticket := s.tickets.Create(crew.Name, crew.Namespace, conversation, question)
	go s.follow(ticket)
	return ticket, nil
}

// follow consumes the discussion stream until the synthesis, then frees the slot.
func (s *Service) follow(ticket *Ticket) {
	defer func() { <-s.inflight }()
	ctx, cancel := context.WithTimeout(context.Background(), followDeadline)
	defer cancel()
	err := s.gateway.Follow(ctx, ticket.Namespace, ticket.Crew, ticket.Conversation, func(ev Event) bool {
		return s.apply(ticket, ev)
	})
	if err != nil {
		log.Error(err, "discussion stream ended", "ticket", ticket.ID, "crew", ticket.Crew)
		ticket.Fail("the discussion stream ended before a synthesis: "+err.Error(), s.now())
		return
	}
	// Fail is a no-op once the ticket is settled, so a stream that already delivered
	// its synthesis (or its error) is left as it is.
	ticket.Fail("the discussion ended without a synthesis", s.now())
}

// apply folds one stream event into the ticket; false stops the follower.
func (s *Service) apply(ticket *Ticket, ev Event) bool {
	switch ev.Type {
	case EventFinding:
		ticket.Contribution(s.now())
	case EventSynthesis:
		ticket.Answer(ev.Content, s.now())
		return false
	case EventError:
		ticket.Fail(ev.Error, s.now())
		return false
	case EventDone:
		return false
	}
	return true
}

// waitFor turns the optional request field into a bounded duration.
func waitFor(seconds *int) time.Duration {
	if seconds == nil {
		return defaultWait
	}
	d := time.Duration(*seconds) * time.Second
	if d < 0 {
		return 0
	}
	if d > maxWait {
		return maxWait
	}
	return d
}

// wait blocks until the ticket settles, the wait elapses, or the client goes away.
func (s *Service) wait(ctx context.Context, ticket *Ticket, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ticket.Done():
	case <-time.After(d):
	case <-ctx.Done():
	}
}

func hintFor(ticket *Ticket) string {
	if ticket.Snapshot().State == StatePending {
		return hintPending
	}
	return ""
}

// toolError is a tool-level failure the model can read and act on, not a protocol error.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
