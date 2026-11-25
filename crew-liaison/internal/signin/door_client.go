package signin

import (
	"context"
	"errors"
)

// clientInfo is what the door knows of a chat app: the redirect addresses it registered and the
// name it gave itself. The name is the app's own claim, shown to the person but never trusted.
type clientInfo struct {
	ID           string
	Name         string
	RedirectURIs []string
}

var errUnknownClient = errors.New("unknown client")

// resolveClient finds the chat app a client_id names. Today that is a client registered by
// dynamic registration, whose id is the sealed registration.
func (d *door) resolveClient(_ context.Context, clientID string) (clientInfo, error) {
	var reg registration
	if clientID == "" || d.sealer.Open(purposeClient, clientID, &reg) != nil {
		return clientInfo{}, errUnknownClient
	}
	return clientInfo{ID: clientID, Name: reg.Name, RedirectURIs: reg.RedirectURIs}, nil
}
