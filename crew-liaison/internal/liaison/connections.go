package liaison

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
)

// StateNeedsConnection is the status of an ask that did not start a discussion because the
// person has to connect an account first.
const StateNeedsConnection State = "needs_connection"

// ConnectionView is one account the person is asked to connect, with the link that does it.
// The link is made by the liaison and carries no token and no personal data.
type ConnectionView struct {
	Account     string `json:"account"`
	DisplayName string `json:"displayName"`
	// State is missing, or needs_reconnect when the service no longer honors a past connection.
	State      string `json:"state"`
	ConnectURL string `json:"connectUrl"`
}

// Accounts is what the tools need from connected accounts. It is nil when the install keeps none.
type Accounts interface {
	// Pending lists the accounts the crew requires that the person has not connected, each
	// with a link. Empty when they are all connected.
	Pending(ctx context.Context, user string, crew Crew) ([]ConnectionView, error)
	// Links returns, for the named accounts the crew declares, the ones the person still has to
	// connect (or connect again), each with a link. An account they have connected since is left out.
	Links(ctx context.Context, user string, crew Crew, names []string) ([]ConnectionView, error)
	// List returns every account the person's crews could use, with their state.
	List(ctx context.Context, user string) ([]AccountView, error)
	// ManageURL is the person's accounts page, where they connect and disconnect.
	ManageURL() string
}

// AccountView is one account in my_accounts.
type AccountView struct {
	Account     string    `json:"account"`
	DisplayName string    `json:"displayName"`
	State       string    `json:"state"`
	Scopes      []string  `json:"scopes,omitempty"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
	Crews       []string  `json:"crews,omitempty"`
	// ConnectURL is set when there is something to connect: not connected, or to be connected again.
	ConnectURL string `json:"connectUrl,omitempty"`
}

// LinkMaker makes the link a person opens to connect an account; the door is one.
type LinkMaker interface {
	ConnectURL(principal, account, crew string) (string, error)
}

type connections struct {
	svc       *accounts.Service
	links     LinkMaker
	manageURL string
}

// NewAccounts returns the tools' view of connected accounts over svc. links makes connect
// links and manageURL is the person's accounts page.
func NewAccounts(svc *accounts.Service, links LinkMaker, manageURL string) Accounts {
	return &connections{svc: svc, links: links, manageURL: manageURL}
}

func (c *connections) ManageURL() string { return c.manageURL }

func crewKeyOf(crew Crew) string { return crew.Namespace + "/" + crew.Name }

func (c *connections) Pending(ctx context.Context, user string, crew Crew) ([]ConnectionView, error) {
	return c.needed(ctx, user, crew, func(d accounts.Declaration) bool { return d.Required })
}

func (c *connections) Links(ctx context.Context, user string, crew Crew, names []string) ([]ConnectionView, error) {
	return c.needed(ctx, user, crew, func(d accounts.Declaration) bool { return slices.Contains(names, d.Name) })
}

// needed returns, for the crew's declared accounts that keep selects, the ones the person must
// connect (or connect again), each with a link.
func (c *connections) needed(
	ctx context.Context, user string, crew Crew, keep func(accounts.Declaration) bool,
) ([]ConnectionView, error) {
	key := crewKeyOf(crew)
	declared, err := c.svc.Decls.Declared(ctx, key)
	if err != nil {
		return nil, err
	}
	principal := c.svc.PrincipalID(user)
	var out []ConnectionView
	for _, d := range declared {
		if !keep(d) {
			continue
		}
		view, needed, err := c.need(ctx, principal, d, key)
		if err != nil {
			return nil, err
		}
		if needed {
			out = append(out, view)
		}
	}
	return out, nil
}

// need says whether the person must connect the account for the crew, with the link.
func (c *connections) need(
	ctx context.Context, principal string, d accounts.Declaration, crew string,
) (ConnectionView, bool, error) {
	if _, ok := c.svc.Regs[d.Name]; !ok {
		return ConnectionView{}, false, nil
	}
	reason, err := c.svc.Broker.Check(ctx, principal, d.Name, crew)
	if err != nil {
		return ConnectionView{}, false, err
	}
	if reason == "" {
		return ConnectionView{}, false, nil
	}
	link, err := c.links.ConnectURL(principal, d.Name, crew)
	if err != nil {
		return ConnectionView{}, false, err
	}
	state := accounts.StateMissing
	if reason == accounts.ReasonNeedsReconnect {
		state = accounts.StateNeedsReconnect
	}
	return ConnectionView{Account: d.Name, DisplayName: d.DisplayName, State: state, ConnectURL: link}, true, nil
}

func (c *connections) List(ctx context.Context, user string) ([]AccountView, error) {
	principal := c.svc.PrincipalID(user)
	entries, err := c.svc.Overview(ctx, principal)
	if err != nil {
		return nil, err
	}
	out := make([]AccountView, 0, len(entries))
	for _, e := range entries {
		v := AccountView{
			Account: e.Account, DisplayName: e.DisplayName, State: e.State, Scopes: e.Scopes,
			ConnectedAt: e.GrantedAt, Crews: e.Crews,
		}
		if e.State != accounts.StateConnected && len(e.Crews) > 0 {
			v.ConnectURL, _ = c.links.ConnectURL(principal, e.Account, e.Crews[0])
		}
		out = append(out, v)
	}
	return out, nil
}

// connectVerb is how the liaison words the step for one account: connect it, or connect it
// again when a past connection no longer works.
func connectVerb(v ConnectionView) string {
	if v.State == accounts.StateNeedsReconnect {
		return "connect again"
	}
	return "connect"
}

// connectPhrase is one account's step with its link, as the liaison writes it.
func connectPhrase(v ConnectionView) string {
	return fmt.Sprintf("%s your %s account: %s", connectVerb(v), v.DisplayName, v.ConnectURL)
}

// connectSentence is the sentence the liaison writes for a person who has to connect accounts,
// built from the links the liaison made so a model never has to copy a link. prefix leads it.
func connectSentence(prefix string, views []ConnectionView) string {
	parts := make([]string, 0, len(views))
	for _, v := range views {
		parts = append(parts, connectPhrase(v))
	}
	return prefix + strings.Join(parts, "\nand ") + "\nThen ask again."
}

// connectLine is the sentence for a question that cannot start until the accounts are connected.
func connectLine(views []ConnectionView) string { return connectSentence("To use this crew, ", views) }

// midwayLine is the sentence for a running discussion that was refused an account.
const midwayOpening = "The crew could not use an account of yours for this question. To get a complete answer, "

func midwayLine(views []ConnectionView) string {
	return connectSentence(midwayOpening, views)
}

// midway returns the links for the accounts a running or finished discussion was refused for,
// from the crew's gateway turn status. It returns none when connected accounts are off, the
// caller is not signed in at the door, or the gateway cannot say.
func (s *Service) midway(ctx context.Context, ticket *Ticket) []ConnectionView {
	user, ok := doorUserOf(ctx)
	if s.accounts == nil || !ok || ticket.Conversation == "" {
		return nil
	}
	names, err := s.gateway.NeedsAccounts(ctx, ticket.Namespace, ticket.Crew, ticket.Conversation)
	if err != nil || len(names) == 0 {
		if err != nil {
			log.Info("the accounts a discussion needs could not be read", "crew", ticket.Crew)
		}
		return nil
	}
	views, err := s.accounts.Links(ctx, user, Crew{Name: ticket.Crew, Namespace: ticket.Namespace}, names)
	if err != nil {
		log.Info("connect links could not be made", "crew", ticket.Crew)
		return nil
	}
	return views
}

// withLinks returns the tool result for out with the liaison's own connect line after the
// output's JSON, so a chat app shows the link whatever it does with structured content. The
// crew's answer is in out, unchanged.
func withLinks(out any, views []ConnectionView) *mcp.CallToolResult {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: string(raw)}, &mcp.TextContent{Text: midwayLine(views)},
	}}
}

// WithAccounts lets the tools check connected accounts before a question starts, and returns
// the service. nil keeps the tools as they were.
func (s *Service) WithAccounts(a Accounts) *Service {
	s.accounts = a
	return s
}

// preflight checks that the person has connected what the crew requires. It answers a result
// to return when they have not; nil means the question may start.
func (s *Service) preflight(
	ctx context.Context, req *mcp.CallToolRequest, crew Crew, question string,
) (*mcp.CallToolResult, AskOutput) {
	if s.accounts == nil {
		return nil, AskOutput{}
	}
	user, ok := doorUserOf(ctx)
	if !ok {
		return nil, AskOutput{}
	}
	if err := s.checkEcho(req, user, crew, question); err != nil {
		return toolError(err.Error()), AskOutput{}
	}
	views, err := s.accounts.Pending(ctx, user, crew)
	if err != nil {
		log.Info("connected accounts could not be checked", "crew", crew.Name)
		return toolError("your connected accounts could not be checked just now; ask again in a moment"), AskOutput{}
	}
	if len(views) == 0 {
		return nil, AskOutput{}
	}
	out := AskOutput{
		TicketView:  TicketView{Crew: crew.Name, Namespace: crew.Namespace, State: StateNeedsConnection},
		Connections: views,
	}
	if res := s.elicitResult(req, user, crew, question, views); res != nil {
		return res, out
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: connectLine(views)}}}, out
}

// MyAccountsInput has no fields.
type MyAccountsInput struct{}

// MyAccountsOutput lists the person's accounts.
type MyAccountsOutput struct {
	Accounts []AccountView `json:"accounts"`
	// ManageURL is the page where the person connects and disconnects.
	ManageURL string `json:"manageUrl,omitempty"`
}

// MyAccounts lists every outside account the person's crews could use and whether it is
// connected, so they can connect before asking anything.
func (s *Service) MyAccounts(
	ctx context.Context, _ *mcp.CallToolRequest, _ MyAccountsInput,
) (*mcp.CallToolResult, MyAccountsOutput, error) {
	user, ok := doorUserOf(ctx)
	if s.accounts == nil || !ok {
		return toolError("connected accounts are not available to you here"), MyAccountsOutput{}, nil
	}
	list, err := s.accounts.List(ctx, user)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, MyAccountsOutput{}, err
		}
		return toolError("your accounts could not be listed just now; try again in a moment"), MyAccountsOutput{}, nil
	}
	return nil, MyAccountsOutput{Accounts: list, ManageURL: s.accounts.ManageURL()}, nil
}

func myAccountsSchema() *jsonschema.Schema { return inputSchema[MyAccountsInput](nil) }
