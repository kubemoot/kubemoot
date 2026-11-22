// What the Moot view shows of ratification: the draft the coordinator put to the table, the
// replies to it (which are verdicts on the draft, not findings), and the answer released with
// the objections that stand. Pure functions of a message, so the view and its tests agree.
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import type { Ratification, RatificationObjection } from '#lib/types/bus.js';

/** The round a reply answers, or null when the message is not a reply to a draft. */
export function ratifiesRound(msg: DiscussionMessage): number | null {
	const round = msg.metadata?.ratifies;
	return typeof round === 'number' && round > 0 ? round : null;
}

/** The badge text of a message: a reply to a draft says so and names the round. */
export function badgeLabel(msg: DiscussionMessage, label: string): string {
	const round = ratifiesRound(msg);
	return round === null ? label : `Ratify: ${label} (round ${round})`;
}

/** One line on a draft: which round of how many, and who was asked. */
export function draftSummary(msg: DiscussionMessage): string | null {
	if (msg.messageType !== 'draft') return null;
	const round = msg.metadata?.round ?? 1;
	const max = msg.metadata?.maxRounds;
	const asked = msg.metadata?.roster ?? [];
	const of = typeof max === 'number' ? ` of ${max}` : '';
	return asked.length > 0 ? `Round ${round}${of}. Asked: ${asked.join(', ')}.` : `Round ${round}${of}.`;
}

/** The objections a revision answers; empty for the first draft and for any other message. */
export function draftObjections(msg: DiscussionMessage): RatificationObjection[] {
	return msg.messageType === 'draft' ? (msg.metadata?.objections ?? []) : [];
}

export type RatificationTone = 'ok' | 'warn' | 'alert';

export interface RatificationView {
	text: string;
	tone: RatificationTone;
	/** The agents that did not answer, so the answer went out without their check. */
	failures: string[];
}

function roundsText(rounds: number): string {
	return rounds === 1 ? '1 round' : `${rounds} rounds`;
}

function ratifiedText(r: Ratification): string {
	const revised = r.rounds > 1 ? ' after a revision' : '';
	return `Ratified by ${r.roster.length} in ${roundsText(r.rounds)}${revised}`;
}

function objectedText(r: Ratification): string {
	const n = r.objections?.length ?? 0;
	return `Released with ${n} objection${n === 1 ? '' : 's'} standing after ${roundsText(r.rounds)}`;
}

function absentText(r: Ratification): string {
	return `Not ratified: ${(r.failures ?? []).join(', ')} did not answer`;
}

/** How the table answered the draft, on the released answer (the answer text itself lists the objections); null when it was not ratified (off, direct, fallback). */
export function ratificationView(msg: DiscussionMessage): RatificationView | null {
	const r: Ratification | undefined = msg.messageType === 'synthesis' ? msg.metadata?.ratification : undefined;
	if (!r) return null;
	const failures = r.failures ?? [];
	if (r.state === 'ratified') return { text: ratifiedText(r), tone: 'ok', failures };
	if (r.state === 'released_with_objections') return { text: objectedText(r), tone: 'alert', failures };
	return { text: absentText(r), tone: 'warn', failures };
}
