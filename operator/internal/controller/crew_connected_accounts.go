/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// A Crew declares the outside accounts a person may connect (spec.connectedAccounts); the
// install registers its own client with each service. The ConnectedAccounts condition says
// whether a declaration can work: its name is registered, no other crew uses the name for a
// different service, and the namespace has the one tool gateway that carries the token.
// It is informational and never gates the Crew's Ready.
const (
	conditionTypeConnectedAccounts = "ConnectedAccounts"
	reasonConnectedAccountsReady   = "Ready"
	reasonUnregisteredAccount      = "UnregisteredAccount"
	reasonAccountConflict          = "Conflict"
	reasonUnknownAccount           = "UnknownAccount"

	// ConnectedAccountsRegisteredEnv lists, comma separated, the account names this install
	// has registered a client for.
	ConnectedAccountsRegisteredEnv = "KUBEMOOT_CONNECTED_ACCOUNTS"
)

// RegisteredAccounts is the set of account names an install has registered.
type RegisteredAccounts map[string]struct{}

// RegisteredAccountsFromEnv reads the names from ConnectedAccountsRegisteredEnv. Blank
// entries are ignored; an unset variable registers none.
func RegisteredAccountsFromEnv(getenv func(string) string) RegisteredAccounts {
	out := RegisteredAccounts{}
	for _, name := range strings.Split(getenv(ConnectedAccountsRegisteredEnv), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// accountsOf indexes a crew's declarations by name.
func accountsOf(crew *kubemootv1alpha1.Crew) map[string]kubemootv1alpha1.ConnectedAccount {
	out := make(map[string]kubemootv1alpha1.ConnectedAccount, len(crew.Spec.ConnectedAccounts))
	for _, a := range crew.Spec.ConnectedAccounts {
		out[a.Name] = a
	}
	return out
}

// sameService reports whether two declarations of one name describe the same service: the
// same endpoints and the same hosts the token may be sent to, so one name never carries two
// send allowlists.
func sameService(a, b kubemootv1alpha1.ConnectedAccount) bool {
	return a.AuthorizeURL == b.AuthorizeURL && a.TokenURL == b.TokenURL &&
		a.RevocationURL == b.RevocationURL && sameHosts(a.TokenHosts, b.TokenHosts)
}

// sameHosts compares two host lists as sets.
func sameHosts(a, b []string) bool {
	as, bs := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	return slices.Equal(as, bs)
}

// reconcileConnectedAccounts records the ConnectedAccounts condition. A crew that declares
// no account carries no condition. A failure to read is returned and leaves the last
// condition in place.
func (r *CrewReconciler) reconcileConnectedAccounts(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	unknown, err := r.unknownAccountServers(ctx, crew.Namespace)
	if err != nil {
		return err
	}
	if len(crew.Spec.ConnectedAccounts) == 0 && len(unknown) == 0 {
		meta.RemoveStatusCondition(&crew.Status.Conditions, conditionTypeConnectedAccounts)
		return nil
	}
	status, reason, message, err := r.connectedAccountsState(ctx, crew, unknown)
	if err != nil {
		return err
	}
	setCrewCondition(crew, conditionTypeConnectedAccounts, status, reason, message)
	return nil
}

// connectedAccountsReadyMessage is the message of a crew whose accounts are ready.
const connectedAccountsReadyMessage = "Every declared account is registered by this install and no other crew uses its name for a different service."

// connectedAccountsState judges one check at a time, most basic first: a name that means two
// services, a name the install has not registered, a server's account no crew declares, then the wiring that carries the token.
func (r *CrewReconciler) connectedAccountsState(ctx context.Context, crew *kubemootv1alpha1.Crew, unknown []string) (metav1.ConditionStatus, string, string, error) {
	conflict, err := r.accountConflict(ctx, crew)
	if err != nil {
		return "", "", "", err
	}
	if conflict != "" {
		return metav1.ConditionFalse, reasonAccountConflict, conflict, nil
	}
	if missing := r.unregisteredAccounts(crew); len(missing) > 0 {
		return metav1.ConditionFalse, reasonUnregisteredAccount, fmt.Sprintf(
			"This install has no registration for %s; the account cannot be connected until the install registers it.",
			quoteNames(missing)), nil
	}
	if len(unknown) > 0 {
		return metav1.ConditionFalse, reasonUnknownAccount, fmt.Sprintf(
			"No crew of this namespace declares the account a server takes: %s. Declare it in a crew's spec.connectedAccounts.",
			strings.Join(unknown, ", ")), nil
	}
	return r.connectedAccountsWiring(ctx, crew)
}

// unknownAccountServers lists, sorted, the servers of the namespace whose callerCredential
// account no crew of the namespace declares, as "server (account)".
func (r *CrewReconciler) unknownAccountServers(ctx context.Context, namespace string) ([]string, error) {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, servers, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	crews := &kubemootv1alpha1.CrewList{}
	if err := r.List(ctx, crews, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	declared := map[string]struct{}{}
	for i := range crews.Items {
		for name := range accountsOf(&crews.Items[i]) {
			declared[name] = struct{}{}
		}
	}
	var unknown []string
	for i := range servers.Items {
		account := serverAccount(&servers.Items[i])
		if _, ok := declared[account]; account != "" && !ok {
			unknown = append(unknown, fmt.Sprintf("%s (%q)", servers.Items[i].Name, account))
		}
	}
	sort.Strings(unknown)
	return unknown, nil
}

// unregisteredAccounts are the crew's declared names the install has not registered, sorted.
func (r *CrewReconciler) unregisteredAccounts(crew *kubemootv1alpha1.Crew) []string {
	var missing []string
	for _, a := range crew.Spec.ConnectedAccounts {
		if _, ok := r.RegisteredAccounts[a.Name]; !ok {
			missing = append(missing, a.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

// accountConflict describes the first of the crew's names (in name order) that another crew
// declares for a different service, or "" when none does. One name never means two services,
// because the install keeps one registration and one token per person per name.
func (r *CrewReconciler) accountConflict(ctx context.Context, crew *kubemootv1alpha1.Crew) (string, error) {
	crews := &kubemootv1alpha1.CrewList{}
	if err := r.List(ctx, crews); err != nil {
		return "", err
	}
	mine := accountsOf(crew)
	names := make([]string, 0, len(mine))
	for name := range mine {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if other := conflictingCrew(crews.Items, crew, mine[name]); other != "" {
			return fmt.Sprintf(
				"Account %q is declared by crew %s for a different service (another endpoint or another set of tokenHosts); "+
					"one name must mean one service, so give one of them another name.", name, other), nil
		}
	}
	return "", nil
}

// conflictingCrew is the first crew, by namespace and name, other than self that declares
// the account's name for a different service, as namespace/name; "" when none does.
func conflictingCrew(crews []kubemootv1alpha1.Crew, self *kubemootv1alpha1.Crew, account kubemootv1alpha1.ConnectedAccount) string {
	var found []string
	for i := range crews {
		other := &crews[i]
		if other.Namespace == self.Namespace && other.Name == self.Name || !other.DeletionTimestamp.IsZero() {
			continue
		}
		if theirs, ok := accountsOf(other)[account.Name]; ok && !sameService(account, theirs) {
			found = append(found, other.Namespace+"/"+other.Name)
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// connectedAccountsWiring checks that a tool server of the namespace that takes one of the
// crew's accounts has the single tool gateway to carry its token. With no such server there
// is nothing to wire and the declaration is ready.
func (r *CrewReconciler) connectedAccountsWiring(ctx context.Context, crew *kubemootv1alpha1.Crew) (metav1.ConditionStatus, string, string, error) {
	servers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, servers, client.InNamespace(crew.Namespace)); err != nil {
		return "", "", "", err
	}
	mine := accountsOf(crew)
	uses := false
	for i := range servers.Items {
		if account := serverAccount(&servers.Items[i]); account != "" {
			_, declared := mine[account]
			uses = uses || declared
		}
	}
	if uses {
		gateway, err := toolGatewayAccount(ctx, r.Client, crew.Namespace)
		if err != nil {
			return "", "", "", err
		}
		if gateway == "" {
			return metav1.ConditionFalse, reasonNoSingleToolGateway,
				"The namespace needs exactly one MCPGateway for the servers that take the crew's accounts; it has none or several.", nil
		}
		return r.accountLeaseHolder(ctx, crew)
	}
	return metav1.ConditionTrue, reasonConnectedAccountsReady,
		connectedAccountsReadyMessage, nil
}

// accountLeaseHolder checks that this is the only crew of the namespace the tool gateway leases
// from, because the tool gateway has one broker address.
func (r *CrewReconciler) accountLeaseHolder(ctx context.Context, crew *kubemootv1alpha1.Crew) (metav1.ConditionStatus, string, string, error) {
	crews := &kubemootv1alpha1.CrewList{}
	if err := r.List(ctx, crews, client.InNamespace(crew.Namespace)); err != nil {
		return "", "", "", err
	}
	if other := otherLeasingCrew(crews.Items, crew); other != "" {
		return metav1.ConditionFalse, reasonSeveralTokenCrews, fmt.Sprintf(
			"Crew %s also keeps tokens or declares accounts; the namespace's tool gateway leases from one crew, "+
				"so keep them in one crew per namespace.", other), nil
	}
	return metav1.ConditionTrue, reasonConnectedAccountsReady,
		connectedAccountsReadyMessage, nil
}

// otherLeasingCrew is the name of the first crew, by name, other than self that answers the tool
// gateway's lease; "" when none does.
func otherLeasingCrew(crews []kubemootv1alpha1.Crew, self *kubemootv1alpha1.Crew) string {
	var found []string
	for i := range crews {
		if crews[i].Name != self.Name && crews[i].DeletionTimestamp.IsZero() && crewLeases(&crews[i]) {
			found = append(found, crews[i].Name)
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// serverAccount is the connected account a server takes the person's token from, or "".
func serverAccount(s *kubemootv1alpha1.MCPServer) string {
	if !takesPersonsToken(s) {
		return ""
	}
	return s.Spec.Registry.CallerCredential.Account
}

// quoteNames renders names as a quoted, comma separated list.
func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// enqueueCrewsSharingAccounts reconciles every other Crew, in any namespace, that declares an
// account name the changed Crew declares, because a conflict between them appears and clears
// with either one. An update maps both the old and the new object, so a renamed account
// reaches the crews of the old name too.
func enqueueCrewsSharingAccounts(cli client.Reader) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return crewsSharingAccounts(ctx, cli, obj)
	})
}

// crewsSharingAccounts is the mapping behind enqueueCrewsSharingAccounts.
func crewsSharingAccounts(ctx context.Context, cli client.Reader, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*kubemootv1alpha1.Crew)
	if !ok || len(changed.Spec.ConnectedAccounts) == 0 {
		return nil
	}
	crews := &kubemootv1alpha1.CrewList{}
	if err := cli.List(ctx, crews); err != nil {
		return nil
	}
	names := accountsOf(changed)
	var reqs []reconcile.Request
	for i := range crews.Items {
		other := &crews.Items[i]
		if other.Namespace == changed.Namespace && other.Name == changed.Name {
			continue
		}
		for name := range accountsOf(other) {
			if _, shared := names[name]; shared {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(other)})
				break
			}
		}
	}
	return reqs
}
