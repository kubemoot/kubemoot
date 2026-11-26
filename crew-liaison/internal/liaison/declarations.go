package liaison

import (
	"context"
	"fmt"
	"sort"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
)

// crewDeclarations reads the connected accounts Crews declare. It is read on each call: the
// liaison caches nothing about who may use what, so a Crew edit takes effect at once.
type crewDeclarations struct {
	reader client.Reader
}

// NewDeclarations returns the accounts' declarations as the Crew resources state them.
func NewDeclarations(reader client.Reader) accounts.Declarations {
	return &crewDeclarations{reader: reader}
}

func crewKey(c *kubemootv1alpha1.Crew) string { return c.Namespace + "/" + c.Name }

// All implements accounts.Declarations: one entry per distinct service a name is declared for
// (its endpoints and token hosts), each with the crews that declare it that way. Which entry
// the install may act on is the accounts package's decision, from the crews it allows.
func (d *crewDeclarations) All(ctx context.Context) ([]accounts.Declaration, error) {
	var crews kubemootv1alpha1.CrewList
	if err := d.reader.List(ctx, &crews); err != nil {
		return nil, fmt.Errorf("listing crews: %w", err)
	}
	sort.Slice(crews.Items, func(i, j int) bool { return crewKey(&crews.Items[i]) < crewKey(&crews.Items[j]) })
	byVariant := map[string]*accounts.Declaration{}
	var order []string
	for i := range crews.Items {
		c := &crews.Items[i]
		for _, a := range c.Spec.ConnectedAccounts {
			fresh := declarationOf(a)
			key := fresh.Name + "\x00" + fresh.EndpointKey()
			decl, seen := byVariant[key]
			if !seen {
				decl = &fresh
				byVariant[key] = decl
				order = append(order, key)
			}
			decl.Crews = append(decl.Crews, crewKey(c))
		}
	}
	out := make([]accounts.Declaration, 0, len(order))
	for _, key := range order {
		out = append(out, *byVariant[key])
	}
	return out, nil
}

func declarationOf(a kubemootv1alpha1.ConnectedAccount) accounts.Declaration {
	return accounts.Declaration{
		Name: a.Name, DisplayName: a.DisplayName, Purpose: a.Purpose,
		Required: a.Usage != kubemootv1alpha1.ConnectedAccountOptional,
		Scopes:   a.Scopes, AuthorizeURL: a.AuthorizeURL, TokenURL: a.TokenURL,
		RevocationURL: a.RevocationURL, TokenHosts: a.TokenHosts,
	}
}

// Declared implements accounts.Declarations: the accounts one crew ("namespace/name") declares.
func (d *crewDeclarations) Declared(ctx context.Context, crew string) ([]accounts.Declaration, error) {
	all, err := d.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []accounts.Declaration
	for _, decl := range all {
		for _, c := range decl.Crews {
			if c == crew {
				out = append(out, decl)
			}
		}
	}
	return out, nil
}
