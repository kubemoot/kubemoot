// The commands that replace the dashboard's former write controls. The dashboard only
// reads; a change is made with kmctl or CrewForge under the caller's own
// kubeconfig identity. kubectl remains only where kmctl has no command. Each builder returns text the page shows and offers to copy.

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

/** Pause, resume or stop a suite. */
export function suiteControlCommand(control: SuiteAction, suite: string, namespace?: string | null): string {
	return `kmctl fitness ${control} ${shellQuote(suite)}${inNamespace(namespace)}`;
}

/** Pin an administrator verdict on an MCPServerReport; change `use` to `caution` or `avoid`. */
export function reportVerdictCommand(report: string, namespace?: string | null): string {
	return `kmctl mcp report pin ${shellQuote(report)} --verdict use${inNamespace(namespace)}`;
}

/** Clear the pinned verdict of an MCPServerReport, so the controller's own verdict shows again. */
export function reportUnpinCommand(report: string, namespace?: string | null): string {
	return `kmctl mcp report unpin ${shellQuote(report)}${inNamespace(namespace)}`;
}

/** Ask a past conversation's first question again. */
export function replayConversationCommand(crew: string, conversationId: string, namespace?: string | null): string {
	return `kmctl conversation replay ${shellQuote(crew)} ${shellQuote(conversationId)}${inNamespace(namespace)}`;
}

/** Add a fact to a crew's memory. */
export function memoryAddCommand(crew: string, namespace?: string | null): string {
	return `kmctl memory add ${shellQuote(crew)} TOPIC KEY "value"${inNamespace(namespace)}`;
}

/** Replace the value of a fact the crew already holds. */
export function memoryEditCommand(crew: string, namespace?: string | null): string {
	return `kmctl memory edit ${shellQuote(crew)} TOPIC KEY "new value"${inNamespace(namespace)}`;
}

/** Delete one fact. */
export function memoryDeleteCommand(crew: string, namespace?: string | null): string {
	return `kmctl memory delete ${shellQuote(crew)} TOPIC KEY${inNamespace(namespace)}`;
}

/** Delete every fact of a crew. */
export function memoryClearCommand(crew: string, namespace?: string | null): string {
	return `kmctl memory clear ${shellQuote(crew)}${inNamespace(namespace)}`;
}

/** Export a crew's memory (read only). */
export function memoryExportCommand(crew: string, namespace?: string | null): string {
	return `kmctl memory export ${shellQuote(crew)}${inNamespace(namespace)}`;
}
