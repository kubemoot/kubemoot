package api

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// conversationRoute is one route that names a conversation or a stored result of one.
// {id} stands for the conversation and {key} for a workspace key of its thread.
type conversationRoute struct {
	method, pattern string
	// owner is what the person who started the conversation gets.
	owner int
	// body is sent with the request.
	body string
}

// The conversation routes and what each answers. A new route that names a conversation must
// be added here; TestEveryConversationRouteIsInTheOwnershipMatrix fails until it is, so no
// route ships without its owner check being asserted.
var conversationRoutes = []conversationRoute{
	{http.MethodGet, "/{id}/stream", http.StatusOK, ""},
	{http.MethodGet, "/{id}/state", http.StatusOK, ""},
	{http.MethodGet, "/workspace?key={key}", http.StatusOK, ""},
	{http.MethodGet, "/artifact?key={key}", http.StatusOK, ""},
	{http.MethodPost, "/{id}/pause", http.StatusConflict, ""},
	{http.MethodPost, "/{id}/resume", http.StatusConflict, ""},
	{http.MethodPost, "/{id}/stop", http.StatusOK, ""},
	{http.MethodDelete, "/{id}", http.StatusOK, ""},
}

// registeredRoutes reads the routes RegisterRoutes names, as "METHOD /suffix" after the crew.
// A source that yields too few routes fails the test, so a renamed or moved registration cannot
// leave the matrix checking nothing.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	re := regexp.MustCompile(`mux\.HandleFunc\("(GET|POST|PUT|DELETE) /api/v1/discussions/\{crew\}(/[^"]*)?"`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		suffix := strings.ReplaceAll(m[2], "{conversationId}", "{id}")
		found[m[1]+" /"+strings.TrimPrefix(suffix, "/")] = true
	}
	if len(found) < minRegisteredRoutes {
		t.Fatalf("read %d routes from handler.go, want at least %d: the registration moved", len(found), minRegisteredRoutes)
	}
	return found
}

// minRegisteredRoutes is how many crew routes handler.go registers today.
const minRegisteredRoutes = 12

// Routes the matrix does not cover, each with the reason and the test that holds it. The test
// names are checked to exist.
var routesOutsideTheMatrix = map[string]struct{ why, test string }{
	"POST /":               {"asking claims or continues one conversation", "TestOnlyTheOwnerMayContinueAConversation"},
	"GET /":                {"lists only the caller's conversations", "TestListShowsOwnConversationsOnly"},
	"GET /events":          {"hears only the caller's conversations", "TestACallerHearsOnlyOfTheirOwnConversations"},
	"PUT /{id}/credential": {"needs a gateway that keeps tokens", "TestOnlyTheOwnerMayRenew"},
}

func TestEveryConversationRouteIsInTheOwnershipMatrix(t *testing.T) {
	routes := registeredRoutes(t)
	covered := map[string]bool{}
	for _, r := range conversationRoutes {
		covered[r.method+" /"+strings.TrimPrefix(strings.SplitN(r.pattern, "?", 2)[0], "/")] = true
	}
	for route := range routes {
		if _, outside := routesOutsideTheMatrix[route]; !outside && !covered[route] {
			t.Errorf("route %q is neither in conversationRoutes nor in routesOutsideTheMatrix", route)
		}
	}
	for route := range covered {
		if !routes[route] {
			t.Errorf("conversationRoutes has %q, which is not a registered route", route)
		}
	}
	tests := testNames(t)
	for route, why := range routesOutsideTheMatrix {
		if !routes[route] {
			t.Errorf("routesOutsideTheMatrix has %q, which is not a registered route", route)
		}
		if !tests[why.test] {
			t.Errorf("%q names %s, which is not a test", route, why.test)
		}
	}
}

// testNames are the names of the tests in this package's test files.
func testNames(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			names[m[1]] = true
		}
	}
	return names
}

func (c controlSetup) request(t *testing.T, r conversationRoute, token string, header ...string) int {
	t.Helper()
	path := strings.NewReplacer("{id}", convCtl, "{key}", objectName).Replace(r.pattern)
	return do(t, r.method, c.srv.URL+listPath+path, token, r.body, header...).StatusCode
}

// Every conversation route answers a caller who does not own the conversation exactly as it
// answers one that does not exist, an operator with the role only with a reason, and the owner
// as the route does. A refused call changes nothing.
func TestEveryConversationRouteAppliesTheOwnerRule(t *testing.T) {
	for _, r := range conversationRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			c := newControlSetup(t)
			c.seed(t, tokenAlice, convCtl, threadCtl, objectName, factKey, false)
			coordinatorThatStops(t, c.js)
			before := c.held(t, convCtl, threadCtl, objectName, factKey)

			for _, tc := range []struct {
				who    string
				token  string
				header []string
				want   int
			}{
				{"non-owner", tokenBob, nil, http.StatusNotFound},
				{"read-only operator", tokenReader, []string{reasonHeader, reasonText}, notFoundUnlessReading(r)},
				{"operator without reason", tokenAdmin, nil, http.StatusBadRequest},
				{"caller outside the crew", tokenNoRole, nil, http.StatusForbidden},
				{"anonymous", "", nil, http.StatusUnauthorized},
			} {
				if got := c.request(t, r, tc.token, tc.header...); got != tc.want {
					t.Errorf("%s: %d, want %d", tc.who, got, tc.want)
				}
			}
			if after := c.held(t, convCtl, threadCtl, objectName, factKey); sum(after) != sum(before) {
				t.Fatalf("a refused call changed the stores: %v, was %v", after, before)
			}
			if got := c.request(t, r, tokenAlice); got != r.owner {
				t.Errorf("the owner: %d, want %d", got, r.owner)
			}
		})
	}
}

// notFoundUnlessReading is what an operator holding only the read role gets: a read is served
// with a reason, anything that changes the conversation is answered as not found.
func notFoundUnlessReading(r conversationRoute) int {
	if r.method == http.MethodGet {
		return http.StatusOK
	}
	return http.StatusNotFound
}
