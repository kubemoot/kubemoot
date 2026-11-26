package liaison

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A client that declared URL elicitation in the request itself (revision 2026-07-28 carries its
// capabilities on every request) is sent to the connect page by the protocol, not by a line of
// text: ask answers input_required with one url-mode elicitation per account to connect, and a
// requestState only this liaison can open. The client opens the link, then retries the same
// call with the state echoed back.
//
// Clients on earlier revisions are never offered this. The door serves each request on its own
// (stateless), so the capabilities a client sent once at initialize are not known on a later
// call, and sending -32042 to a client that never declared the capability would be wrong; they
// get the connect link in the result.

// StateSealer seals and opens the state a client echoes back. The door's sealer is one.
type StateSealer interface {
	Seal(purpose string, v any) (string, error)
	Open(purpose, blob string, v any) error
}

const (
	purposeElicit = "liaison-connect-elicit"
	stateLife     = 10 * time.Minute
	idBytes       = 8
)

// elicitState is what the requestState holds. It names the person and the call, so a state
// copied to another person's call, or to another question, is refused.
type elicitState struct {
	User    string `json:"u"`
	Crew    string `json:"c"`
	Digest  string `json:"d"`
	ID      string `json:"i"`
	Expires int64  `json:"e"`
}

// errStaleState is the refusal of a requestState that is not valid for this call.
var errStaleState = errors.New("this request is no longer valid; ask again")

// WithStateSealer lets ask send a client that can open links the connect page as an
// elicitation, and returns the service. nil keeps the link in the result for every client.
func (s *Service) WithStateSealer(sealer StateSealer) *Service {
	s.sealer = sealer
	return s
}

// digestOf is the fingerprint of the call a state belongs to.
func digestOf(crew Crew, question string) string {
	sum := sha256.Sum256([]byte(crew.Namespace + "\x00" + crew.Name + "\x00" + question))
	return hex.EncodeToString(sum[:])
}

// echoedState is the requestState the client sent back, "" on a first call.
func echoedState(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	return req.Params.RequestState
}

// checkEcho refuses a requestState that is not this person's, for this call, unexpired and
// ours. A call with none, or a liaison that issues none, passes.
func (s *Service) checkEcho(req *mcp.CallToolRequest, user string, crew Crew, question string) error {
	blob := echoedState(req)
	if blob == "" || s.sealer == nil {
		return nil
	}
	var st elicitState
	if s.sealer.Open(purposeElicit, blob, &st) != nil || st.User != user ||
		st.Crew != crewKeyOf(crew) || st.Digest != digestOf(crew, question) || s.now().Unix() > st.Expires {
		return errStaleState
	}
	return nil
}

// elicitResult is the input_required answer that sends the client to each connect link. It
// returns nil when this client is not offered one, so the caller answers with the link text.
func (s *Service) elicitResult(
	req *mcp.CallToolRequest, user string, crew Crew, question string, views []ConnectionView,
) *mcp.CallToolResult {
	if s.sealer == nil || !clientElicitsURL(req) || !retriesCalls(req) {
		return nil
	}
	id, err := randomID()
	if err != nil {
		return nil
	}
	state, err := s.sealer.Seal(purposeElicit, elicitState{
		User: user, Crew: crewKeyOf(crew), Digest: digestOf(crew, question), ID: id,
		Expires: s.now().Add(stateLife).Unix(),
	})
	if err != nil {
		return nil
	}
	inputs := mcp.InputRequestMap{}
	for _, v := range views {
		inputs["connect_"+v.Account] = &mcp.ElicitParams{
			Mode: "url", URL: v.ConnectURL, ElicitationID: id + "-" + v.Account,
			Message: elicitMessage(v),
		}
	}
	return &mcp.CallToolResult{InputRequests: inputs, RequestState: state}
}

// elicitMessage is the sentence the client shows beside the link, written here.
func elicitMessage(v ConnectionView) string {
	verb := connectVerb(v)
	return fmt.Sprintf("%s%s your %s account so the crew can use it for your question.",
		strings.ToUpper(verb[:1]), verb[1:], v.DisplayName)
}

// retriesCalls reports whether the client is on a revision whose clients retry an
// input_required answer by themselves: the revision that carries capabilities per request.
func retriesCalls(req *mcp.CallToolRequest) bool {
	if req == nil || req.Params == nil {
		return false
	}
	version, _ := req.Params.GetMeta()[mcp.MetaKeyProtocolVersion].(string)
	return version >= revisionMultiRoundTrip
}

// revisionMultiRoundTrip is the first revision with input_required results and a retry.
const revisionMultiRoundTrip = "2026-07-28"

func randomID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
