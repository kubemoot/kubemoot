package accounts

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
)

// ErrConflict means the crews the install allows declare one account for different services,
// so no one of them can be trusted with the install's client secret.
var ErrConflict = errors.New("the crews allowed to use the account declare it for different services")

// Offer is an account as one person can connect it: the declaration the install acts on, the
// install's registration, and the crews that will use it.
type Offer struct {
	Declaration  Declaration
	Registration Registration
	// Crews are the crews that declare the account and that the install allows.
	Crews []string
}

// EndpointKey names the service a declaration points at: its three endpoints and token hosts.
// Two declarations with one key declare one service.
func (d Declaration) EndpointKey() string {
	hosts := slices.Clone(d.TokenHosts)
	sort.Strings(hosts)
	return strings.Join([]string{d.AuthorizeURL, d.TokenURL, d.RevocationURL, strings.Join(hosts, ",")}, "\x00")
}

// resolve picks the declaration the install may act on. Only declarations by crews the
// registration allows count, so a crew the install did not allow can never supply the
// endpoints the client secret, the code and the refresh tokens are sent to. A registration
// that pins hosts drops a declaration with an endpoint elsewhere. When the allowed crews
// still disagree, nothing is offered.
func resolve(all []Declaration, name string, reg Registration) (Offer, error) {
	var chosen *Offer
	for _, d := range all {
		if d.Name != name || !reg.PinsAllow(d) {
			continue
		}
		crews := reg.AllowedAmong(d.Crews)
		if len(crews) == 0 {
			continue
		}
		if chosen != nil && chosen.Declaration.EndpointKey() != d.EndpointKey() {
			return Offer{}, ErrConflict
		}
		if chosen == nil {
			d.Crews = nil
			chosen = &Offer{Declaration: d, Registration: reg}
		}
		chosen.Crews = append(chosen.Crews, crews...)
	}
	if chosen == nil {
		return Offer{}, ErrNotAllowed
	}
	chosen.Declaration.Crews = chosen.Crews
	return *chosen, nil
}

// offerFor is the one place an account becomes an Offer: the install registered it, and the
// crews that declare it and are allowed agree on the service. Not found and conflict are
// ErrNotAllowed and ErrConflict; a failure to read the declarations is returned as it is.
func offerFor(ctx context.Context, decls Declarations, regs map[string]Registration, name string) (Offer, error) {
	reg, ok := regs[name]
	if !ok {
		return Offer{}, ErrNotAllowed
	}
	all, err := decls.All(ctx)
	if err != nil {
		return Offer{}, err
	}
	return resolve(all, name, reg)
}

// offerForCrew is offerFor when the one crew named must be among the offer's crews.
func offerForCrew(
	ctx context.Context, decls Declarations, regs map[string]Registration, name, crew string,
) (Offer, error) {
	o, err := offerFor(ctx, decls, regs, name)
	if err != nil {
		return Offer{}, err
	}
	if !slices.Contains(o.Crews, crew) {
		return Offer{}, ErrNotAllowed
	}
	return o, nil
}

// unreadable reports whether err is a failure to read, not an answer about the account.
func unreadable(err error) bool {
	return err != nil && !errors.Is(err, ErrNotAllowed) && !errors.Is(err, ErrConflict)
}
