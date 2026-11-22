import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import {
	badgeLabel,
	draftObjections,
	draftSummary,
	ratificationView,
	ratifiesRound
} from './ratification-view.js';

function msg(messageType: string, metadata: Record<string, unknown> | undefined): DiscussionMessage {
	return {
		messageId: 'm1',
		threadId: 't1',
		agentName: 'a',
		messageType,
		content: 'c',
		metadata
	} as unknown as DiscussionMessage;
}

const objection = { agent: 'compute', verdict: 'block', round: 1, content: 'there are 14 nodes' };

describe('a reply to a draft', () => {
	it('names the round and is told apart from a finding', () => {
		expect(ratifiesRound(msg('concern', { ratifies: 2 }))).toBe(2);
		expect(ratifiesRound(msg('concern', {}))).toBeNull();
		expect(ratifiesRound(msg('concern', undefined))).toBeNull();
		expect(badgeLabel(msg('concern', { ratifies: 1 }), 'Concern')).toBe('Ratify: Concern (round 1)');
		expect(badgeLabel(msg('agree', {}), 'Agree')).toBe('Agree');
	});

	it('a zero or malformed round is not a reply to a draft', () => {
		expect(ratifiesRound(msg('agree', { ratifies: 0 }))).toBeNull();
		expect(ratifiesRound(msg('agree', { ratifies: 'one' }))).toBeNull();
	});
});

describe('a draft', () => {
	it('says the round, the limit and who was asked', () => {
		const d = msg('draft', { round: 1, maxRounds: 2, roster: ['k8s-nodes', 'compute'] });
		expect(draftSummary(d)).toBe('Round 1 of 2. Asked: k8s-nodes, compute.');
		expect(draftSummary(msg('draft', {}))).toBe('Round 1.');
		expect(draftSummary(msg('agree', { round: 1 }))).toBeNull();
	});

	it('a revision carries the objections it answers', () => {
		expect(draftObjections(msg('draft', { round: 2, objections: [objection] }))).toEqual([objection]);
		expect(draftObjections(msg('draft', { round: 1 }))).toEqual([]);
		expect(draftObjections(msg('synthesis', { objections: [objection] }))).toEqual([]);
	});
});

describe('the released answer', () => {
	it('shows a ratified answer, and a revision it took', () => {
		const first = ratificationView(msg('synthesis', { ratification: { state: 'ratified', rounds: 1, roster: ['a', 'b'] } }));
		expect(first?.text).toBe('Ratified by 2 in 1 round');
		expect(first?.tone).toBe('ok');
		const revised = ratificationView(msg('synthesis', { ratification: { state: 'ratified', rounds: 2, roster: ['a'] } }));
		expect(revised?.text).toBe('Ratified by 1 in 2 rounds after a revision');
	});

	it('counts the objections that stand', () => {
		const view = ratificationView(
			msg('synthesis', {
				ratification: { state: 'released_with_objections', rounds: 2, roster: ['a'], objections: [objection] }
			})
		);
		expect(view?.text).toBe('Released with 1 objection standing after 2 rounds');
		expect(view?.tone).toBe('alert');
	});

	it('names the agents that did not answer', () => {
		const view = ratificationView(
			msg('synthesis', { ratification: { state: 'not_ratified', rounds: 1, roster: ['a', 'b'], failures: ['b'] } })
		);
		expect(view?.text).toBe('Not ratified: b did not answer');
		expect(view?.tone).toBe('warn');
		expect(view?.failures).toEqual(['b']);
	});

	it('is absent for an answer nobody ratified', () => {
		expect(ratificationView(msg('synthesis', {}))).toBeNull();
		expect(ratificationView(msg('synthesis', undefined))).toBeNull();
		expect(ratificationView(msg('agree', { ratification: { state: 'ratified', rounds: 1, roster: [] } }))).toBeNull();
	});
});
