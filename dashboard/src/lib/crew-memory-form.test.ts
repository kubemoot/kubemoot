import { describe, expect, it } from 'vitest';
import { canSaveFact, factFormProblem, trimmedDraft } from './crew-memory-form';

const scope = { namespace: 'pilot', crew: 'homelab-pilot' };
const full = { topic: 'gpu-topology', key: 'rig0', value: '5090' };

describe('memory fact form', () => {
	it('enables Save when a crew is picked and every field is filled', () => {
		expect(canSaveFact(scope, full)).toBe(true);
		expect(factFormProblem(scope, full)).toBeNull();
	});

	it('stays disabled, and says why, with All crews selected', () => {
		expect(canSaveFact(null, full)).toBe(false);
		expect(factFormProblem(null, full)).toMatch(/Pick one crew/);
	});

	it.each([
		['topic', { ...full, topic: '' }, /topic/],
		['key', { ...full, key: '' }, /key/],
		['value', { ...full, value: '' }, /value/]
	])('names the missing %s', (_n, draft, re) => {
		expect(canSaveFact(scope, draft)).toBe(false);
		expect(factFormProblem(scope, draft)).toMatch(re);
	});

	it('treats whitespace-only fields as empty', () => {
		expect(canSaveFact(scope, { ...full, value: '   ' })).toBe(false);
	});

	it('trims what is sent', () => {
		expect(trimmedDraft({ topic: ' a ', key: ' b', value: 'c ' })).toEqual({ topic: 'a', key: 'b', value: 'c' });
	});
});
