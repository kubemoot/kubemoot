import { describe, it, expect } from 'vitest';
import {
	CHAT_ALL,
	DISCUSS_ALL,
	agentNodeId,
	agentStateFor,
	agentStateKey,
	artifactObjectKey,
	chatNamespaceFilter,
	chatSubject,
	crewMemoryKey,
	crewMemoryPrefix,
	crewScope,
	discussCrewFilter,
	discussNamespaceFilter,
	discussSubject,
	discussThreadFilter,
	kvPrefixSubject,
	parseAgentStateKey,
	parseArtifactKey,
	parseChatSubject,
	parseCrewMemoryKey,
	parseDiscussSubject,
	tryCrewScope
} from './crewScope';

const THREAD = '0f8fad5b-d9cb-469f-a165-70867728950e';
const alpha = crewScope('team-alpha', 'homelab-pilot');
const beta = crewScope('team-beta', 'homelab-pilot');

describe('crewScope', () => {
	it('validates namespace and crew', () => {
		expect(alpha).toEqual({ namespace: 'team-alpha', crew: 'homelab-pilot' });
	});

	it.each([
		['', 'crew'],
		[undefined, 'crew'],
		[null, 'crew'],
		['Team-Alpha', 'crew'],
		['team.alpha', 'crew'],
		['-team', 'crew'],
		['a'.repeat(64), 'crew'],
		['team-alpha', ''],
		['team-alpha', undefined],
		['team-alpha', 'crew.name'],
		['team-alpha', 'crew*'],
		['team-alpha', '>'],
		['team-alpha', 'my crew']
	])('rejects namespace %j with crew %j', (ns, crew) => {
		expect(() => crewScope(ns, crew)).toThrow();
		expect(tryCrewScope(ns, crew)).toBeNull();
	});

	it('accepts a 63-character namespace', () => {
		expect(tryCrewScope('a'.repeat(63), 'c')).not.toBeNull();
	});
});

describe('discussion subjects', () => {
	it('builds the namespaced 5-token form', () => {
		expect(discussSubject(alpha, 'general', THREAD)).toBe(
			`kubemoot.discuss.team-alpha.homelab-pilot.general.${THREAD}`
		);
	});

	it('gives the same crew name in two namespaces distinct subjects and filters', () => {
		expect(discussSubject(alpha, 'general', THREAD)).not.toBe(discussSubject(beta, 'general', THREAD));
		expect(discussCrewFilter(alpha)).toBe('kubemoot.discuss.team-alpha.homelab-pilot.>');
		expect(discussCrewFilter(beta)).toBe('kubemoot.discuss.team-beta.homelab-pilot.>');
		expect(discussNamespaceFilter('team-beta')).toBe('kubemoot.discuss.team-beta.>');
	});

	it('builds thread purge filters, scoped and unscoped', () => {
		expect(discussThreadFilter(THREAD, alpha)).toBe(`kubemoot.discuss.team-alpha.homelab-pilot.*.${THREAD}`);
		expect(discussThreadFilter(THREAD)).toBe(`kubemoot.discuss.*.*.*.${THREAD}`);
		expect(discussThreadFilter(THREAD, null)).toBe(`kubemoot.discuss.*.*.*.${THREAD}`);
	});

	it('round-trips build and parse', () => {
		const subject = discussSubject(beta, 'infra', THREAD);
		expect(parseDiscussSubject(subject)).toEqual({
			namespace: 'team-beta',
			crew: 'homelab-pilot',
			channel: 'infra',
			threadId: THREAD
		});
	});

	it.each([
		undefined,
		null,
		'',
		DISCUSS_ALL,
		// the pre-namespace 4-token form
		`kubemoot.discuss.homelab-pilot.general.${THREAD}`,
		`kubemoot.discuss.general.${THREAD}`,
		`kubemoot.discuss.team-alpha.homelab-pilot.general.${THREAD}.extra`,
		`kubemoot.chat.team-alpha.homelab-pilot.general.${THREAD}`,
		`kubemoot.discuss.Team.homelab-pilot.general.${THREAD}`,
		`kubemoot.discuss.team-alpha.*.general.${THREAD}`,
		`kubemoot.discuss.team-alpha.homelab-pilot..${THREAD}`
	])('rejects malformed discuss subject %j', (subject) => {
		expect(parseDiscussSubject(subject)).toBeNull();
	});

	it('refuses to build with a missing or wildcard part', () => {
		expect(() => discussSubject(alpha, '', THREAD)).toThrow();
		expect(() => discussSubject(alpha, 'general', '*')).toThrow();
		expect(() => discussSubject({ namespace: '', crew: 'x' }, 'general', THREAD)).toThrow();
		expect(() => discussNamespaceFilter('')).toThrow();
		expect(() => discussThreadFilter('>')).toThrow();
	});
});

describe('chat subjects', () => {
	it('builds and parses kubemoot.chat.<ns>.<agent>', () => {
		expect(chatSubject('team-alpha', 'coordinator')).toBe('kubemoot.chat.team-alpha.coordinator');
		expect(parseChatSubject(chatSubject('team-beta', 'coordinator'))).toEqual({
			namespace: 'team-beta',
			agent: 'coordinator'
		});
		expect(chatSubject('team-alpha', 'coordinator')).not.toBe(chatSubject('team-beta', 'coordinator'));
		expect(chatNamespaceFilter('team-alpha')).toBe('kubemoot.chat.team-alpha.>');
		expect(CHAT_ALL).toBe('kubemoot.chat.>');
	});

	it.each([
		undefined,
		'kubemoot.chat.coordinator',
		'kubemoot.chat.team-alpha.coordinator.extra',
		'kubemoot.discuss.team-alpha.coordinator',
		'kubemoot.chat.TEAM.coordinator',
		'kubemoot.chat.team-alpha.>'
	])('rejects malformed chat subject %j', (subject) => {
		expect(parseChatSubject(subject)).toBeNull();
	});

	it('refuses to build without a namespace', () => {
		expect(() => chatSubject('', 'coordinator')).toThrow();
	});
});

describe('agent state keys', () => {
	it('builds and parses <ns>.<agent>', () => {
		expect(agentStateKey('team-alpha', 'coordinator')).toBe('team-alpha.coordinator');
		expect(agentStateKey('team-alpha', 'coordinator')).not.toBe(agentStateKey('team-beta', 'coordinator'));
		expect(parseAgentStateKey('team-beta.coordinator')).toEqual({ namespace: 'team-beta', agent: 'coordinator' });
	});

	it.each([undefined, 'coordinator', 'a.b.c', 'Team.coordinator', 'team.'])('rejects %j', (key) => {
		expect(parseAgentStateKey(key)).toBeNull();
	});

	it('refuses to build without a namespace', () => {
		expect(() => agentStateKey('', 'coordinator')).toThrow();
	});

	it('looks up an entry by namespace AND name', () => {
		const entries = { 'team-alpha.coordinator': 'a', 'team-beta.coordinator': 'b', coordinator: 'old' };
		expect(agentStateFor(entries, 'team-alpha', 'coordinator')).toBe('a');
		expect(agentStateFor(entries, 'team-beta', 'coordinator')).toBe('b');
		expect(agentStateFor(entries, 'team-gamma', 'coordinator')).toBeUndefined();
		expect(agentStateFor(entries, '', 'coordinator')).toBeUndefined();
		expect(agentStateFor(entries, 'team-alpha', undefined)).toBeUndefined();
		expect(agentStateFor(null, 'team-alpha', 'coordinator')).toBeUndefined();
	});
});

describe('crew memory keys', () => {
	it('builds and parses <ns>.<crew>.<topic>.<key>', () => {
		expect(crewMemoryPrefix(alpha)).toBe('team-alpha.homelab-pilot.');
		expect(crewMemoryKey(alpha, 'gpu-topology', 'rig0')).toBe('team-alpha.homelab-pilot.gpu-topology.rig0');
		expect(crewMemoryKey(alpha, 'gpu-topology', 'rig0')).not.toBe(crewMemoryKey(beta, 'gpu-topology', 'rig0'));
		expect(parseCrewMemoryKey(crewMemoryKey(beta, 't', 'k'))).toEqual({
			namespace: 'team-beta',
			crew: 'homelab-pilot',
			topic: 't',
			key: 'k'
		});
	});

	it('builds the KV purge subject for one crew', () => {
		expect(kvPrefixSubject('kubemoot_crew_memory', crewMemoryPrefix(alpha))).toBe(
			'$KV.kubemoot_crew_memory.team-alpha.homelab-pilot.>'
		);
	});

	it.each([undefined, 'homelab-pilot.topic.key', 'a.b.c.d.e', 'Team.crew.t.k', 'ns.crew..k'])(
		'rejects %j',
		(key) => {
			expect(parseCrewMemoryKey(key)).toBeNull();
		}
	);
});

describe('discussion artifact keys', () => {
	it('builds and parses <ns>/<crew>/<thread>/<agent>/<name>', () => {
		const key = artifactObjectKey(alpha, THREAD, 'researcher', 'agree-1234');
		expect(key).toBe(`team-alpha/homelab-pilot/${THREAD}/researcher/agree-1234`);
		expect(key).not.toBe(artifactObjectKey(beta, THREAD, 'researcher', 'agree-1234'));
		expect(parseArtifactKey(key)).toEqual({
			namespace: 'team-alpha',
			crew: 'homelab-pilot',
			threadId: THREAD,
			agent: 'researcher',
			name: 'agree-1234'
		});
	});

	it.each([
		undefined,
		`homelab-pilot/${THREAD}/researcher/agree-1`,
		`/team-alpha/homelab-pilot/${THREAD}/researcher`,
		`team-alpha/homelab-pilot/${THREAD}/researcher/agree-1/`,
		`team-alpha/homelab-pilot/../researcher/agree-1`,
		`team-alpha/homelab-pilot/${THREAD}/re searcher/agree-1`,
		`Team/homelab-pilot/${THREAD}/researcher/agree-1`
	])('rejects %j', (key) => {
		expect(parseArtifactKey(key)).toBeNull();
	});

	it('refuses to build a traversal segment', () => {
		expect(() => artifactObjectKey(alpha, '..', 'a', 'b')).toThrow();
	});
});

describe('agentNodeId', () => {
	it('keeps same-named agents in two namespaces distinct', () => {
		expect(agentNodeId('team-alpha', 'coordinator')).not.toBe(agentNodeId('team-beta', 'coordinator'));
	});
});
