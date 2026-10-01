import { describe, expect, it } from 'vitest';
import type { Agent } from '$types/kubemoot.js';
import { buildChannels, compareChannels, countChannelSubscribers } from './channels';

function agent(...channels: string[]): Agent {
	return { metadata: { name: 'a' }, spec: { a2a: { subscribeChannels: channels } } } as Agent;
}

describe('countChannelSubscribers', () => {
	it('counts each subscribing agent and always includes general', () => {
		const counts = countChannelSubscribers([agent('kubernetes', 'zeta'), agent('kubernetes')]);
		expect([...counts]).toEqual([
			['kubernetes', 2],
			['zeta', 1],
			['general', 0]
		]);
	});

	it('handles agents without a2a config', () => {
		const bare = { metadata: { name: 'b' }, spec: {} } as Agent;
		expect([...countChannelSubscribers([bare])]).toEqual([['general', 0]]);
	});
});

describe('compareChannels', () => {
	const ch = (name: string) => ({ name, color: '', agentCount: 0 });

	it('orders known channels by their fixed order, before any other', () => {
		expect(compareChannels(ch('kubernetes'), ch('general'))).toBeLessThan(0);
		expect(compareChannels(ch('general'), ch('alpha'))).toBeLessThan(0);
		expect(compareChannels(ch('alpha'), ch('proxmox'))).toBeGreaterThan(0);
	});

	it('orders other channels alphabetically', () => {
		expect(compareChannels(ch('beta'), ch('alpha'))).toBeGreaterThan(0);
	});
});

describe('buildChannels', () => {
	it('keeps known colors, assigns the palette to others in first-seen order, and sorts', () => {
		const channels = buildChannels([agent('zeta', 'proxmox', 'alpha')]);
		expect(channels).toEqual([
			{ name: 'proxmox', color: '#8b5cf6', agentCount: 1 },
			{ name: 'general', color: '#6b7280', agentCount: 0 },
			{ name: 'alpha', color: '#14b8a6', agentCount: 1 },
			{ name: 'zeta', color: '#ec4899', agentCount: 1 }
		]);
	});

	it('cycles the palette when there are more dynamic channels than colors', () => {
		const names = ['c1', 'c2', 'c3', 'c4', 'c5', 'c6', 'c7'];
		const seventh = buildChannels([agent(...names)]).find((c) => c.name === 'c7');
		expect(seventh?.color).toBe('#ec4899');
	});
});
