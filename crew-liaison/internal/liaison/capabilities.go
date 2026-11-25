package liaison

import "github.com/modelcontextprotocol/go-sdk/mcp"

// clientElicitsURL reports whether the client behind req declared that it can send a person
// to a URL (the elicitation capability's url mode). The SDK reads it from the request's own
// _meta for revision 2026-07-28 and from the initialize capabilities for older revisions, so
// one check covers both. A client that declared nothing, or form mode only, gets false and
// the caller falls back to a link in its own result.
func clientElicitsURL(req *mcp.CallToolRequest) bool {
	if req == nil {
		return false
	}
	return capabilitiesElicitURL(req.ClientCapabilities())
}

func capabilitiesElicitURL(caps *mcp.ClientCapabilities) bool {
	return caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil
}
