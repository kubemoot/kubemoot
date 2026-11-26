package signin

// Names the tests of the door share.
const (
	keyAuthEndpoint  = "authorization_endpoint"
	keyTokenEndpoint = "token_endpoint"
	keyIssuer        = "issuer"
	evilRedirect     = "https://evil.example.test/cb"
	claimSub         = "sub"
	claimNameKey     = "name"
	groupTeamA       = "oidc:team-a"
	userJane         = "oidc:jane-1"
)

// Names the tests share.
const (
	noStore    = "no-store"
	fetchSite  = "Sec-Fetch-Site"
	evilOrigin = "https://evil.example.test"
	formToken  = "token"
	subJane    = "jane"
	crossSite  = "cross-site"

	paramScopeKey = "scope"
)
