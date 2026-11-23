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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// conversationOwners reads a conversation's owner record. found is false when it has none;
// an error means the record could not be read, which refuses and never allows.
type conversationOwners interface {
	ConversationOwner(ctx context.Context, scope crewscope.Scope, id string) (owner bus.Principal, found bool, err error)
}

// exportAuditLog receives the audit records of operator exports; a test replaces it.
var exportAuditLog logr.Logger = logf.Log.WithName("audit")

// reportCaller is who called a report endpoint, set once the caller passed requireAccess.
type reportCaller struct {
	identity *identity.Identity
	// reason is the Kubemoot-Access-Reason header, empty when none was sent.
	reason string
}

type reportCallerKey struct{}

func withReportCaller(ctx context.Context, c reportCaller) context.Context {
	return context.WithValue(ctx, reportCallerKey{}, c)
}

func reportCallerFrom(ctx context.Context) (reportCaller, bool) {
	c, ok := ctx.Value(reportCallerKey{}).(reportCaller)
	return c, ok && c.identity != nil
}

// mayExportConversation is the one place that decides whether the caller may export a
// conversation. A crew whose gateway verifies no one has no owners, so the access the route
// already checked (get on the Crew) is enough, as it is for a memory export. On a crew that
// verifies callers the owner may export, and an operator who may get crews/conversations may
// export another caller's conversation by giving a reason, which is audited. Everyone else,
// and any check that cannot be made, is refused.
func (s *ReportServer) mayExportConversation(ctx context.Context, scope crewscope.Scope, id string) error {
	crew := &kubemootv1alpha1.Crew{}
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: scope.Namespace, Name: scope.Crew}, crew)
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%w: the crew does not exist", conversationexport.ErrRefused)
	case err != nil:
		return fmt.Errorf("read the crew: %w", err)
	case discussionAccess(crew) == nil:
		return nil
	}
	return s.mayExportAsCaller(ctx, crew, scope, id)
}

// mayExportAsCaller decides the export of a conversation on a crew that verifies callers.
func (s *ReportServer) mayExportAsCaller(ctx context.Context, crew *kubemootv1alpha1.Crew, scope crewscope.Scope, id string) error {
	caller, ok := reportCallerFrom(ctx)
	if !ok || s.Owners == nil || s.Auth == nil {
		return fmt.Errorf("%w: the caller or the owner records cannot be checked", conversationexport.ErrRefused)
	}
	hasher, err := s.callerHasher(ctx, crew, caller.identity)
	if err != nil {
		return err
	}
	owner, found, err := s.Owners.ConversationOwner(ctx, scope, id)
	if err != nil {
		return fmt.Errorf("read the conversation's owner: %w", err)
	}
	if found && identity.SamePrincipal(owner, identity.PrincipalOf(hasher, caller.identity)) {
		return nil
	}
	return s.operatorMayExport(ctx, scope, id, caller, owner.ID)
}

// callerHasher is the hasher the gateway used to record owners for this caller: the crew's
// keyed hasher for a subscriber, so the two principals agree. A Kubernetes caller's id is
// always the unkeyed hash and needs no key. A subscriber whose crew has no readable hash
// key cannot be matched to an owner, so the export is refused.
func (s *ReportServer) callerHasher(ctx context.Context, crew *kubemootv1alpha1.Crew, id *identity.Identity) (identity.Hasher, error) {
	if id.Issuer == identity.IssuerKubernetes {
		return identity.Hasher{}, nil
	}
	name, key, ok := hashKeySecret(discussionAccess(crew))
	if !ok {
		return identity.Hasher{}, fmt.Errorf("%w: the crew has no hash key to name its callers", conversationexport.ErrRefused)
	}
	secret := &corev1.Secret{}
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: crew.Namespace, Name: name}, secret)
	switch {
	case apierrors.IsNotFound(err):
		return identity.Hasher{}, fmt.Errorf("%w: the crew's hash key Secret does not exist", conversationexport.ErrRefused)
	case err != nil:
		return identity.Hasher{}, fmt.Errorf("read the crew's hash key: %w", err)
	}
	if len(secret.Data[key]) == 0 {
		return identity.Hasher{}, fmt.Errorf("%w: the crew's hash key is empty", conversationexport.ErrRefused)
	}
	return identity.Hasher{Key: secret.Data[key]}, nil
}

// operatorMayExport admits a caller who may get the crew's conversations and says why, and
// audits it. The record is written when access is granted, before the export runs.
func (s *ReportServer) operatorMayExport(
	ctx context.Context, scope crewscope.Scope, id string, caller reportCaller, ownerID string,
) error {
	allowed, err := s.Auth.Allowed(ctx, caller.identity, identity.ConversationsAccess(scope.Namespace, scope.Crew, verbGet))
	if err != nil {
		return fmt.Errorf("check the caller's access: %w", err)
	}
	if !allowed {
		return conversationexport.ErrRefused
	}
	if _, ok := identity.ValidateAccessReason(caller.reason); !ok {
		return fmt.Errorf("%w: exporting another caller's conversation needs a reason of 1 to %d characters in the %s header",
			conversationexport.ErrRefused, identity.MaxAccessReason, identity.HeaderAccessReason)
	}
	identity.OperatorAccess{Operator: caller.identity, Operation: "export", Namespace: scope.Namespace, Crew: scope.Crew,
		ConversationID: id, OwnerID: ownerID, Reason: caller.reason}.Audit(exportAuditLog)
	return nil
}
