package liaison

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	exampleName = "Example Library"
	acctMail    = "mail"
)

const connectBase = "https://door.example.test/oauth/connect/"

type stubLinks struct{ err error }

func (l stubLinks) ConnectURL(principal, account, crew string) (string, error) {
	if l.err != nil {
		return "", l.err
	}
	return connectBase + account + "?for=" + crew + "&p=" + principal[:6], nil
}

// stubAccounts is Accounts with fixed answers.
type stubAccounts struct {
	pending []ConnectionView
	list    []AccountView
	err     error
	asked   []string
}

func (s *stubAccounts) Pending(_ context.Context, user string, _ Crew) ([]ConnectionView, error) {
	s.asked = append(s.asked, user)
	return s.pending, s.err
}

func (s *stubAccounts) List(context.Context, string) ([]AccountView, error) { return s.list, s.err }
func (s *stubAccounts) ManageURL() string                                   { return "https://door.example.test/oauth/accounts" }

func doorCtx(t *testing.T) context.Context {
	t.Helper()
	signer, _ := hopKeyPair(t)
	return withAsserter(context.Background(), signer, &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: janeUser})
}

var exampleLibrary = ConnectionView{
	Account: acctLibrary, DisplayName: exampleName, State: accounts.StateMissing, ConnectURL: connectBase + "library?c=h",
}

func TestAskTellsAPersonWhoMustConnectAndStartsNothing(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	stub := &stubAccounts{pending: []ConnectionView{exampleLibrary}}
	svc := newTestService(t, f, 2).WithAccounts(stub)
	res, out, err := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	if err != nil || res == nil {
		t.Fatalf("Ask: res=%v err=%v", res, err)
	}
	if out.State != StateNeedsConnection || len(out.Connections) != 1 || out.Connections[0].ConnectURL != exampleLibrary.ConnectURL || out.ID != "" {
		t.Fatalf("output: %+v", out)
	}
	checkLinkLine(t, res)
	if len(stub.asked) != 1 || stub.asked[0] != janeUser {
		t.Errorf("asked for: %v", stub.asked)
	}
	if f.started() != 0 {
		t.Error("no discussion may start before the connection exists")
	}
}

func checkLinkLine(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "connect your Example Library account: "+exampleLibrary.ConnectURL) || !strings.Contains(text, "ask again") {
		t.Fatalf("the liaison's own link line: %q", text)
	}
	if res.IsError {
		t.Error("needing a connection is an answer, not a failure")
	}
}

func TestAskStartsWhenEverythingIsConnectedOrTheCallerHasNoAccounts(t *testing.T) {
	cases := map[string]struct {
		ctx  func(*testing.T) context.Context
		stub *stubAccounts
	}{
		"all connected":             {func(t *testing.T) context.Context { return doorCtx(t) }, &stubAccounts{}},
		"not signed in at the door": {func(*testing.T) context.Context { return context.Background() }, &stubAccounts{pending: []ConnectionView{exampleLibrary}}},
	}
	for name, c := range cases {
		f := newFakeGateway(synthesisEvents()...)
		svc := newTestService(t, f, 2).WithAccounts(c.stub)
		_, out, err := svc.Ask(c.ctx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)})
		f.srv.Close()
		if err != nil || out.State != StateAnswered {
			t.Errorf("%s: %+v %v", name, out, err)
		}
	}
}

func TestAskWithoutAccountsIsAsItWas(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 2)
	if _, out, _ := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(5)}); out.State != StateAnswered {
		t.Fatalf("%+v", out)
	}
}

func TestACheckThatFailsDoesNotStartTheQuestion(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 2).WithAccounts(&stubAccounts{err: errors.New("store away")})
	res, _, _ := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion})
	if res == nil || !res.IsError || strings.Contains(res.Content[0].(*mcp.TextContent).Text, "store away") {
		t.Fatalf("a failed check: %+v", res)
	}
	if f.started() != 0 {
		t.Error("a question must not start when the connections could not be checked")
	}
}

func TestARejoinSkipsThePreflight(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	stub := &stubAccounts{}
	svc := newTestService(t, f, 2).WithAccounts(stub)
	_, first, _ := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, WaitSeconds: sec(0)})
	stub.pending = []ConnectionView{exampleLibrary}
	_, again, _ := svc.Ask(doorCtx(t), nil, AskInput{Crew: testCrew, Question: testQuestion, Ticket: first.ID, WaitSeconds: sec(0)})
	if again.State == StateNeedsConnection {
		t.Fatal("a question already running is not stopped by a later connection change")
	}
}

func TestConnectLine(t *testing.T) {
	one := connectLine([]ConnectionView{exampleLibrary})
	if one != "To use this crew, connect your Example Library account: "+exampleLibrary.ConnectURL+"\nThen ask again." {
		t.Errorf("one: %q", one)
	}
	two := connectLine([]ConnectionView{
		{DisplayName: "A", State: accounts.StateNeedsReconnect, ConnectURL: "u1"}, {DisplayName: "B", State: accounts.StateMissing, ConnectURL: "u2"},
	})
	if !strings.Contains(two, "connect again your A account: u1") || !strings.Contains(two, "connect your B account: u2") {
		t.Errorf("two: %q", two)
	}
}

func TestMyAccountsListsOnlyForADoorPerson(t *testing.T) {
	svc := NewService(staticLister{}, nil, NewStore(time.Hour), 1)
	stub := &stubAccounts{list: []AccountView{{Account: acctLibrary, DisplayName: exampleName, State: accounts.StateConnected}}}
	svc.WithAccounts(stub)
	res, out, err := svc.MyAccounts(doorCtx(t), nil, MyAccountsInput{})
	if err != nil || res != nil || len(out.Accounts) != 1 || out.ManageURL == "" {
		t.Fatalf("my_accounts: %v %+v %v", res, out, err)
	}
	myAccountsRefusals(t, svc)
	stub.err = errors.New("away")
	if res, _, _ := svc.MyAccounts(doorCtx(t), nil, MyAccountsInput{}); res == nil || !res.IsError {
		t.Error("a failing list")
	}
	stub.err = context.Canceled
	if _, _, err := svc.MyAccounts(doorCtx(t), nil, MyAccountsInput{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled call: %v", err)
	}
}

func myAccountsRefusals(t *testing.T, svc *Service) {
	t.Helper()
	if res, _, _ := svc.MyAccounts(context.Background(), nil, MyAccountsInput{}); res == nil || !res.IsError {
		t.Error("a caller the door did not sign in has no accounts")
	}
	if res, _, _ := NewService(staticLister{}, nil, NewStore(time.Hour), 1).MyAccounts(doorCtx(t), nil, MyAccountsInput{}); res == nil || !res.IsError {
		t.Error("an install with no accounts")
	}
}

func TestMyAccountsIsOnlyAToolWhenAccountsAreOn(t *testing.T) {
	names := func(svc *Service) []string {
		server := NewMCPServer(svc)
		cs, ss := mcp.NewInMemoryTransports()
		go func() { _, _ = server.Connect(context.Background(), ss, nil) }()
		client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
		session, err := client.Connect(context.Background(), cs, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = session.Close() }()
		var out []string
		for tool, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, tool.Name)
		}
		return out
	}
	off := names(NewService(staticLister{}, nil, NewStore(time.Hour), 1))
	on := names(NewService(staticLister{}, nil, NewStore(time.Hour), 1).WithAccounts(&stubAccounts{}))
	if len(on) != len(off)+1 || !contains(on, "my_accounts") || contains(off, "my_accounts") {
		t.Fatalf("off=%v on=%v", off, on)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// accountsRig is the real adapter over a real vault and broker.
type accountsRig struct {
	acc   Accounts
	svc   *accounts.Service
	links stubLinks
}

type testSealer struct{}

func (testSealer) Seal(_ string, v any) (string, error) { return jsonString(v), nil }
func (testSealer) Open(_, blob string, v any) error     { return jsonParse(blob, v) }

func newAccountsRig(t *testing.T) *accountsRig {
	t.Helper()
	store, err := tokenstore.NewSealed(tokenstore.NewMemory(nil), testSealer{}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	regs := map[string]accounts.Registration{acctLibrary: {ClientID: "c"}, acctMail: {ClientID: "m"}}
	vault, _ := accounts.NewVault(store, regs, nil)
	decls := fakeAccountDecls{
		acctLibrary: {Name: acctLibrary, DisplayName: exampleName, Required: true, Crews: []string{guideCrew}, TokenURL: "https://a.example.org/t"},
		acctMail:    {Name: acctMail, DisplayName: "Mail", Required: false, Crews: []string{guideCrew}},
		"hidden":    {Name: "hidden", DisplayName: "Hidden", Required: true, Crews: []string{guideCrew}},
	}
	svc := &accounts.Service{
		People: accounts.NewPrincipals([]byte("k")), Vault: vault, Decls: decls, Regs: regs, Client: accounts.NewClient(nil),
		Now: time.Now, Broker: accounts.NewBroker(vault, decls, regs, accounts.NewClient(nil), nil),
	}
	links := stubLinks{}
	return &accountsRig{acc: NewAccounts(svc, links, "https://door.example.test/oauth/accounts"), svc: svc, links: links}
}

func TestPendingNamesOnlyTheRequiredAccountsNotYetConnected(t *testing.T) {
	r := newAccountsRig(t)
	ctx := context.Background()
	crew := guideCrewValue
	got, err := r.acc.Pending(ctx, janeUser, crew)
	if err != nil || len(got) != 1 || got[0].Account != acctLibrary || got[0].State != accounts.StateMissing ||
		!strings.HasPrefix(got[0].ConnectURL, connectBase+acctLibrary) {
		t.Fatalf("pending: %+v %v", got, err)
	}
	tok := accounts.Token{Access: "a", Refresh: "r", AccessExpires: time.Now().Add(time.Hour), GrantedAt: time.Now(), LastUsed: time.Now(), Crews: []string{guideCrew}}
	if _, err := r.svc.Vault.Save(ctx, r.svc.PrincipalID(janeUser), acctLibrary, tok, tokenstore.AnyRevision); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.acc.Pending(ctx, janeUser, crew); len(got) != 0 {
		t.Fatalf("connected: %+v", got)
	}
	if got, _ := r.acc.Pending(ctx, "oidc:joe", crew); len(got) != 1 {
		t.Fatalf("another person has no connection: %+v", got)
	}
	pendingAfterReconnectMark(t, r, tok)
}

func pendingAfterReconnectMark(t *testing.T, r *accountsRig, tok accounts.Token) {
	t.Helper()
	ctx := context.Background()
	tok.State = accounts.StateNeedsReconnect
	_, _ = r.svc.Vault.Save(ctx, r.svc.PrincipalID(janeUser), acctLibrary, tok, tokenstore.AnyRevision)
	if got, _ := r.acc.Pending(ctx, janeUser, guideCrewValue); len(got) != 1 || got[0].State != accounts.StateNeedsReconnect {
		t.Fatalf("needs reconnect: %+v", got)
	}
}

func TestPendingAndListReportALinkFailure(t *testing.T) {
	r := newAccountsRig(t)
	broken := NewAccounts(r.svc, stubLinks{err: errors.New("no")}, "x")
	if _, err := broken.Pending(context.Background(), janeUser, guideCrewValue); err == nil {
		t.Error("Pending must report a failed link")
	}
	list, err := broken.List(context.Background(), janeUser)
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v %v", list, err)
	}
	for _, a := range list {
		if a.ConnectURL != "" {
			t.Errorf("no link can be made, so none is shown: %+v", a)
		}
	}
}

func TestListShowsConnectedAndNotYet(t *testing.T) {
	r := newAccountsRig(t)
	ctx := context.Background()
	tok := accounts.Token{Access: "a", Refresh: "r", AccessExpires: time.Now().Add(time.Hour), GrantedAt: time.Now(), LastUsed: time.Now(), Crews: []string{guideCrew}, Scopes: []string{"search"}}
	_, _ = r.svc.Vault.Save(ctx, r.svc.PrincipalID(janeUser), acctLibrary, tok, tokenstore.AnyRevision)
	list, err := r.acc.List(ctx, janeUser)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	byName := map[string]AccountView{}
	for _, a := range list {
		byName[a.Account] = a
	}
	checkListed(t, byName[acctLibrary], byName[acctMail])
	if r.acc.ManageURL() == "" {
		t.Error("manage URL")
	}
}

func checkListed(t *testing.T, lib, mail AccountView) {
	t.Helper()
	if lib.State != accounts.StateConnected || lib.ConnectURL != "" || len(lib.Scopes) != 1 || lib.ConnectedAt.IsZero() {
		t.Errorf("connected: %+v", lib)
	}
	if mail.State != accounts.StateMissing || !strings.HasPrefix(mail.ConnectURL, connectBase+acctMail) {
		t.Errorf("not connected: %+v", mail)
	}
}

// started is how many questions the gateway received.
func (f *fakeGateway) started() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.questions)
}

func jsonString(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func jsonParse(blob string, v any) error { return json.Unmarshal([]byte(blob), v) }

var guideCrewValue = Crew{Name: "guide", Namespace: "ns"}
