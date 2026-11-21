/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"strings"
	"unicode/utf8"

	"github.com/go-logr/logr"
)

// AuditEvent is the fixed event name of an operator's access to another caller's data.
const AuditEvent = "kubemoot.conversation.access"

// ValidateAccessReason trims the Kubemoot-Access-Reason header value and reports whether it
// is 1 to MaxAccessReason characters.
func ValidateAccessReason(header string) (reason string, ok bool) {
	reason = strings.TrimSpace(header)
	n := utf8.RuneCountInString(reason)
	return reason, n >= 1 && n <= MaxAccessReason
}

// OperatorAccess is what an audit record of an operator reading another caller's data holds.
// It records access granted, not data delivered: a batch export that later fails still has
// its record. Subject is the operator's own name, which the audit record is for; the owner
// is named by pseudonymous id only.
type OperatorAccess struct {
	Operator       *Identity
	Operation      string
	Namespace      string
	Crew           string
	ConversationID string
	OwnerID        string
	Reason         string
}

// Audit writes the record with the fixed event name, the same fields wherever it is written.
func (a OperatorAccess) Audit(log logr.Logger) {
	log.Info("Operator access to another caller's conversation", "event", AuditEvent,
		"operator", a.Operator.Subject, "operatorIssuer", a.Operator.Issuer, "operation", a.Operation,
		"crew", a.Crew, "namespace", a.Namespace, "conversationId", a.ConversationID,
		"owner", a.OwnerID, "reason", a.Reason)
}
