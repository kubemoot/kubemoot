import { describe, expect, it } from 'vitest';
import type { Agent } from '$types/kubemoot.js';
import { topologyNode, topologyRole, topologyStatus } from './topology';

function agent(spec: object = {}, status?: object, metadata: object = {}): Agent {
	return { metadata: { name: 'sre', ...metadata }, spec, status } as unknown as Agent;
}

describe('topologyRole', () => {
	it('keeps coordinator and specialist and makes anything else a peer', () => {
		expect(topologyRole(agent({ a2a: { role: 'coordinator' } }))).toBe('coordinator');
		expect(topologyRole(agent({ a2a: { role: 'specialist' } }))).toBe('specialist');
		expect(topologyRole(agent({ a2a: { role: 'judge' } }))).toBe('peer');
		expect(topologyRole(agent())).toBe('peer');
	});
});

describe('topologyStatus', () => {
	it('is ready when ready, error on Error or Failed, otherwise pending', () => {
		expect(topologyStatus(agent({}, { ready: true, phase: 'Failed' }))).toBe('ready');
		expect(topologyStatus(agent({}, { phase: 'Error' }))).toBe('error');
		expect(topologyStatus(agent({}, { phase: 'Failed' }))).toBe('error');
		expect(topologyStatus(agent({}, { phase: 'Running' }))).toBe('pending');
		expect(topologyStatus(agent())).toBe('pending');
	});
});

describe('topologyNode', () => {
	it('builds a node with summed tool counts', () => {
		const node = topologyNode(
			agent(
				{ description: 'site reliability', a2a: { role: 'specialist' } },
				{
					ready: true,
					endpoint: 'sre.crew-a.svc:8080',
					mcpServerStatus: [{ toolCount: 3 }, { toolCount: 4 }, {}]
				},
				{ namespace: 'crew-a', labels: { team: 'ops' } }
			)
		);
		expect(node).toEqual({
			id: 'sre',
			namespace: 'crew-a',
			role: 'specialist',
			status: 'ready',
			endpoint: 'sre.crew-a.svc:8080',
			toolCount: 7,
			peerCount: 0,
			description: 'site reliability',
			labels: { team: 'ops' }
		});
	});

	it('fills defaults for a bare agent', () => {
		expect(topologyNode(agent())).toMatchObject({
			namespace: '',
			endpoint: '',
			toolCount: 0,
			description: '',
			labels: {}
		});
	});
});
