import { describe, expect, it } from 'vitest';
import { crewGoes } from './crew-delete';
import type { Crew } from '#lib/types/kubemoot.js';

const crew = (over: Partial<Crew['metadata']> = {}, agentCount?: number): Crew =>
	({ metadata: { name: 'homelab-pilot', namespace: 'crew-a', ...over }, spec: {}, status: { agentCount } }) as unknown as Crew;

describe('crewGoes', () => {
	it('names the crew, its agents, its history and memory, and its bindings', () => {
		expect(crewGoes(crew({}, 23))).toEqual([
			'The crew homelab-pilot in crew-a',
			'Its 23 agents',
			'Its discussion history and working memory',
			'Its role bindings'
		]);
	});

	it('says agent in the singular for one', () => {
		expect(crewGoes(crew({}, 1))[1]).toBe('Its 1 agent');
	});

	it('names no count when the crew has not reported its agents', () => {
		expect(crewGoes(crew())[1]).toBe('Its agents');
	});

	it('adds the namespace only when the crew asked Kubemoot to manage it', () => {
		const text = crewGoes(crew({ annotations: { 'kubemoot.ai/manage-namespace': 'true' } })).join('|');
		expect(text).toContain('Its namespace crew-a, which it asked Kubemoot to manage');
		expect(crewGoes(crew({ annotations: { 'kubemoot.ai/manage-namespace': 'false' } })).join('|')).not.toContain('namespace');
	});

	it('warns that a release which manages the crew may create it again', () => {
		expect(crewGoes(crew({ labels: { 'app.kubernetes.io/managed-by': 'Helm' } })).at(-1)).toContain('may create it again');
	});
});
