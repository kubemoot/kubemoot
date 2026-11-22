// Acting on a conversation from the dashboard, as the signed-in person: stop, pause, resume and
// delete. The browser calls the dashboard server, which sends the call to the crew's discussion
// gateway with the person's own token; the gateway's answer, or its refusal, comes back here.
// Nothing in this file sees the token.

import { resolve } from '$app/paths';
import type { CrewScope } from '#lib/crewScope.js';
import { AskRefused, refusalOf } from '#lib/ask-client.js';
import { REASON_HEADER, validReason } from '#lib/access-reason.js';

/** What a conversation's running discussion can be told, as the gateway's routes name them. */
export const CONTROLS = ['stop', 'pause', 'resume'] as const;
export type Control = (typeof CONTROLS)[number];


/** What a delete removed from each store, which stores failed, and what cannot be removed. */
export interface ErasureReport {
	conversationId: string;
	threads?: string[];
	deleted: Record<string, number>;
	kept: string[];
	failed?: string[];
}

/** A delete that left a store uncleared: the gateway's report says which, and a repeat removes the rest. */
export class ErasureIncomplete extends Error {
	constructor(readonly report: ErasureReport) {
		super('Some stores could not be cleared.');
		this.name = 'ErasureIncomplete';
	}
}

const conversationPath = (s: CrewScope, id: string) =>
	resolve('/api/gateway/[namespace]/[crew]/[conversationId]', { namespace: s.namespace, crew: s.crew, conversationId: id });

function headersFor(reason: string | undefined): Record<string, string> {
	const clean = validReason(reason);
	return clean ? { [REASON_HEADER]: clean } : {};
}

/** True when the refusal says an operator must give a reason to act on this conversation. */
export function needsReason(err: unknown): boolean {
	return err instanceof AskRefused && err.status === 400 && /reason/i.test(err.message);
}

/** True when the delete was refused because a discussion is still running. */
export function isStillRunning(err: unknown): boolean {
	return err instanceof AskRefused && err.status === 409;
}

async function send(fetchFn: typeof fetch, url: string, init: RequestInit): Promise<Response> {
	try {
		return await fetchFn(url, init);
	} catch {
		throw new AskRefused(0, 'The dashboard server could not be reached. Check the connection and try again.');
	}
}

/** Stops, pauses or resumes the running discussion of a conversation. Throws AskRefused with the reason shown. */
export async function controlConversation(
	fetchFn: typeof fetch,
	scope: CrewScope,
	conversationId: string,
	control: Control,
	reason?: string
): Promise<void> {
	const res = await send(fetchFn, `${conversationPath(scope, conversationId)}/${control}`, {
		method: 'POST',
		headers: headersFor(reason)
	});
	if (!res.ok) throw await refusalOf(res);
}

function isReport(body: unknown): body is ErasureReport {
	return typeof body === 'object' && body !== null && typeof (body as ErasureReport).deleted === 'object';
}

/**
 * Deletes a conversation from every store that holds it and returns the gateway's report. A
 * running conversation is a 409 AskRefused unless force is set, which stops it first. A store
 * that could not be cleared throws ErasureIncomplete with the report.
 */
export async function deleteConversation(
	fetchFn: typeof fetch,
	scope: CrewScope,
	conversationId: string,
	options: { force?: boolean; reason?: string } = {}
): Promise<ErasureReport> {
	const query = options.force ? '?force=true' : '';
	const res = await send(fetchFn, conversationPath(scope, conversationId) + query, {
		method: 'DELETE',
		headers: headersFor(options.reason)
	});
	if (res.status === 500) {
		const body: unknown = await res.clone().json().catch(() => null);
		if (isReport(body)) throw new ErasureIncomplete(body);
	}
	if (!res.ok) throw await refusalOf(res);
	const body: unknown = await res.json().catch(() => null);
	if (!isReport(body)) throw new AskRefused(502, 'The crew deleted the conversation but sent no report.');
	return body;
}
