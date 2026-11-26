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

// All implements accounts.Declarations. When two crews declare one name, the first (by crew
// name) supplies the display text and endpoints; the operator reports a conflict on the Crew
// when they differ, so one name does not mean two services.
func (d *crewDeclarations) All(ctx context.Context) ([]accounts.Declaration, error) {
	var crews kubemootv1alpha1.CrewList
	if err := d.reader.List(ctx, &crews); err != nil {
		return nil, fmt.Errorf("listing crews: %w", err)
	}
	sort.Slice(crews.Items, func(i, j int) bool { return crewKey(&crews.Items[i]) < crewKey(&crews.Items[j]) })
	byName := map[string]*accounts.Declaration{}
	var order []string
	for i := range crews.Items {
		c := &crews.Items[i]
		for _, a := range c.Spec.ConnectedAccounts {
			decl, seen := byName[a.Name]
			if !seen {
				fresh := declarationOf(a)
				decl = &fresh
				byName[a.Name] = decl
				order = append(order, a.Name)
			}
			decl.Crews = append(decl.Crews, crewKey(c))
		}
	}
	out := make([]accounts.Declaration, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
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

// Account implements accounts.Declarations.
func (d *crewDeclarations) Account(ctx context.Context, name string) (accounts.Declaration, bool, error) {
	all, err := d.All(ctx)
	if err != nil {
		return accounts.Declaration{}, false, err
	}
	for _, decl := range all {
		if decl.Name == name {
			return decl, true, nil
		}
	}
	return accounts.Declaration{}, false, nil
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
