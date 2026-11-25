package signin

import "errors"

// clientInfo is what the door knows of a chat app: the redirect addresses it registered and the
// name it gave itself. The name is the app's own claim, shown to the person but never trusted.
type clientInfo struct {
	ID           string
	Name         string
	RedirectURIs []string
}

var errUnknownClient = errors.New("unknown client")
