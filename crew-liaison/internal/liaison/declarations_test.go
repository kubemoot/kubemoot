package liaison

import (
	"context"
	"errors"
	"testing"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func crewWith(ns, name string, accts ...kubemootv1alpha1.ConnectedAccount) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       kubemootv1alpha1.CrewSpec{ConnectedAccounts: accts},
	}
}

func account(name, display string, usage kubemootv1alpha1.ConnectedAccountUsage) kubemootv1alpha1.ConnectedAccount {
	return kubemootv1alpha1.ConnectedAccount{
		Name: name, DisplayName: display, Usage: usage, AuthorizeURL: "https://auth.example.org/a",
		TokenURL: "https://auth.example.org/t", TokenHosts: []string{apiHost}, Scopes: []string{"search"},
	}
}

func newDecls(t *testing.T, crews ...*kubemootv1alpha1.Crew) *crewDeclarations {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, c := range crews {
		b = b.WithObjects(c)
	}
	return &crewDeclarations{reader: b.Build()}
}

func TestDeclarationsGatherTheCrewsThatDeclareOneName(t *testing.T) {
	d := newDecls(t,
		crewWith("b", "two", account("library", "Library", "")),
		crewWith("a", "one", account("library", "Library", kubemootv1alpha1.ConnectedAccountOptional), account("mail", "Mail", "")),
		crewWith("c", "none"),
	)
	ctx := context.Background()
	all, err := d.All(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %+v %v", all, err)
	}
	lib, found, _ := d.Account(ctx, "library")
	if !found || len(lib.Crews) != 2 || lib.Crews[0] != "a/one" || lib.Crews[1] != "b/two" {
		t.Fatalf("library: %+v", lib)
	}
	checkDefaults(t, d, lib)
	got, _ := d.Declared(ctx, "a/one")
	if len(got) != 2 {
		t.Errorf("accounts of a/one: %+v", got)
	}
	if none, _ := d.Declared(ctx, "c/none"); len(none) != 0 {
		t.Errorf("accounts of a crew with none: %+v", none)
	}
}

func checkDefaults(t *testing.T, d *crewDeclarations, lib accounts.Declaration) {
	t.Helper()
	ctx := context.Background()
	if lib.Required {
		t.Error("the first crew's declaration (Optional) supplies the text")
	}
	if mail, _, _ := d.Account(ctx, "mail"); !mail.Required || mail.TokenHosts[0] != apiHost {
		t.Errorf("a default usage is Required: %+v", mail)
	}
	if _, found, err := d.Account(ctx, "nothing"); found || err != nil {
		t.Errorf("an account no crew declares: %v %v", found, err)
	}
}

func TestDeclarationsReportAListFailure(t *testing.T) {
	d := &crewDeclarations{reader: failingReader{}}
	if _, err := d.All(context.Background()); err == nil {
		t.Fatal("a failing list must be an error")
	}
	if _, _, err := d.Account(context.Background(), "x"); err == nil {
		t.Fatal("Account must pass the error on")
	}
	if _, err := d.Declared(context.Background(), "a/b"); err == nil {
		t.Fatal("Declared must pass the error on")
	}
}

// failingReader is a Kubernetes reader whose list always fails.
type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("api server away")
}
