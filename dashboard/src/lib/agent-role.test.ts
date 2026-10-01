import { describe, expect, it } from 'vitest';
import type { Agent } from '$types/kubemoot.js';
import { agentRole } from './agent-role';

function agent(labels?: Record<string, string>, annotations?: Record<string, string>): Agent {
	return { metadata: { name: 'a', labels, annotations }, spec: {} } as unknown as Agent;
}

describe('agentRole', () => {
	it('returns the labelled role', () => {
		for (const role of ['coordinator', 'tooler', 'analyst', 'researcher', 'specialist']) {
			expect(agentRole(agent({ 'kubemoot.ai/role': role }))).toBe(role);
		}
	});

	it('keeps a labelled role even on a system-annotated agent', () => {
		const a = agent({ 'kubemoot.ai/role': 'analyst' }, { 'kubemoot.ai/rtfm-mode': 'true' });
		expect(agentRole(a)).toBe('analyst');
	});

	it('treats onboarding, RTFM, and observer agents as system', () => {
		const role = { 'kubemoot.ai/role': 'other' };
		expect(agentRole(agent(role, { 'kubemoot.ai/onboarding-mode': 'true' }))).toBe('system');
		expect(agentRole(agent(role, { 'kubemoot.ai/rtfm-mode': 'true' }))).toBe('system');
		expect(agentRole(agent(role, { 'kubemoot.ai/discuss-role': 'observer' }))).toBe('system');
	});

	it('treats an agent without the role label as system', () => {
		expect(agentRole(agent())).toBe('system');
		expect(agentRole(agent({ 'kubemoot.ai/role': '' }))).toBe('system');
	});

	it('shows an unrecognised role label as a tooler', () => {
		expect(agentRole(agent({ 'kubemoot.ai/role': 'other' }))).toBe('tooler');
		expect(
			agentRole(agent({ 'kubemoot.ai/role': 'other' }, { 'kubemoot.ai/rtfm-mode': 'false' }))
		).toBe('tooler');
	});
});
