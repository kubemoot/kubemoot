package liaison

import (
	"context"
	"errors"
	"testing"

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

func TestDeclarationsGatherCrewsThatDeclareOneService(t *testing.T) {
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
	if all[0].Name != "library" || len(all[0].Crews) != 2 || all[0].Crews[0] != "a/one" || all[0].Crews[1] != "b/two" {
		t.Fatalf("library: %+v", all[0])
	}
	if all[0].Required || !all[1].Required || all[1].TokenHosts[0] != apiHost {
		t.Errorf("the first crew supplies the text, and a default usage is Required: %+v", all)
	}
	checkDeclared(t, d)
}

func checkDeclared(t *testing.T, d *crewDeclarations) {
	t.Helper()
	got, _ := d.Declared(context.Background(), "a/one")
	if len(got) != 2 {
		t.Errorf("accounts of a/one: %+v", got)
	}
	if none, _ := d.Declared(context.Background(), "c/none"); len(none) != 0 {
		t.Errorf("accounts of a crew with none: %+v", none)
	}
}

func TestDeclarationsReportAListFailure(t *testing.T) {
	d := &crewDeclarations{reader: failingReader{}}
	if _, err := d.All(context.Background()); err == nil {
		t.Fatal("a failing list must be an error")
	}
	if _, err := d.Declared(context.Background(), "a/b"); err == nil {
		t.Fatal("Declared must pass the error on")
	}
}

func TestCrewsThatDeclareOneNameForDifferentServicesAreKeptApart(t *testing.T) {
	evil := account("library", "Library", "")
	evil.TokenURL = "https://evil.example.net/token"
	d := newDecls(t, crewWith("a", "aaa", evil), crewWith("b", "guide", account("library", "Library", "")))
	all, err := d.All(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatalf("two services under one name must be two entries: %+v %v", all, err)
	}
	if all[0].Crews[0] != "a/aaa" || all[0].TokenURL != evil.TokenURL || all[1].Crews[0] != "b/guide" {
		t.Fatalf("each entry carries its own crews and endpoints: %+v", all)
	}
}

// failingReader is a Kubernetes reader whose list always fails.
type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("api server away")
}

func (failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("api server away")
}
