// The commands that replace the dashboard's former write controls. The dashboard only
// reads; a change is made with kmctl, CrewForge or kubectl under the caller's own
// kubeconfig identity. Each builder returns text the page shows and offers to copy.

import type { SuiteAction } from '#lib/fitness-suite-controls.js';

const SAFE_WORD = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** Quotes one shell word, leaving plain names as they are. */
export function shellQuote(word: string): string {
	if (word !== '' && SAFE_WORD.test(word)) return word;
	const escaped = word.replaceAll("'", String.raw`'\''`);
	return `'${escaped}'`;
}

function inNamespace(namespace: string | undefined | null): string {
	return namespace ? ` -n ${shellQuote(namespace)}` : '';
}

/** Ask a crew a question; with a conversation id, a follow-up in that conversation. */
export function askCommand(crew: string, namespace?: string | null, conversationId?: string): string {
	const follow = conversationId ? ` --conversation-id ${shellQuote(conversationId)}` : '';
	return `kmctl conversation ask ${shellQuote(crew)} "your question"${follow}${inNamespace(namespace)}`;
}

/** List a crew's conversations, to find the id a follow-up needs. */
export function listConversationsCommand(crew: string, namespace?: string | null): string {
	return `kmctl conversation list ${shellQuote(crew)}${inNamespace(namespace)}`;
}

/** Remove a crew; the operator's finalizer removes its Agents and open discussions. */
export function deleteCrewCommand(crew: string, namespace?: string | null): string {
	return `kmctl delete crew ${shellQuote(crew)}${inNamespace(namespace)}`;
}

/** Remove a fitness suite run; its owned CrewFitness resources go with it. */
export function deleteSuiteCommand(suite: string, namespace?: string | null): string {
	return `kmctl delete crewfitnesssuite ${shellQuote(suite)}${inNamespace(namespace)}`;
}

const SUITE_PATCH: Record<SuiteAction, string> = {
	pause: '{"spec":{"suspend":true}}',
	resume: '{"spec":{"suspend":false}}',
	stop: '{"spec":{"cancel":true}}'
};

/** Pause, resume or stop a suite by setting spec.suspend or spec.cancel. */
export function suiteControlCommand(control: SuiteAction, suite: string, namespace?: string | null): string {
	return `kubectl patch crewfitnesssuite ${shellQuote(suite)} --type=merge -p '${SUITE_PATCH[control]}'${inNamespace(namespace)}`;
}

/** Pin or clear an administrator verdict on an MCPServerReport. */
export function reportVerdictCommand(report: string, namespace?: string | null): string {
	const patch = '{"spec":{"adminVerdict":"use","adminNotes":"why","adminAuthor":"you"}}';
	return `kubectl patch mcpserverreport ${shellQuote(report)} --type=merge -p '${patch}'${inNamespace(namespace)}`;
}

/** Export a crew's memory (read only). */
export function memoryExportCommand(crew: string, namespace?: string | null): string {
	return `kmctl memory export ${shellQuote(crew)}${inNamespace(namespace)}`;
}
