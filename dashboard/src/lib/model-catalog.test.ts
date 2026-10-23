import { describe, expect, it } from 'vitest';
import type { ModelServer } from '#lib/types/kubemoot.js';
import { catalogEntries, contextLabel, filterCatalog, parameterLabel } from './model-catalog.js';

function server(name: string, models: { name: string }[] | undefined): ModelServer {
	return {
		apiVersion: 'kubemoot.ai/v1alpha1',
		kind: 'ModelServer',
		metadata: { name, namespace: 'kubemoot' },
		spec: { type: 'vllm' },
		status: models ? { models } : undefined
	} as ModelServer;
}

describe('catalogEntries', () => {
	it('lists each server catalog model, sorted by model then server', () => {
		const out = catalogEntries([server('b', [{ name: 'z' }, { name: 'a' }]), server('a', [{ name: 'a' }])]);
		expect(out.map((e) => `${e.model.name}@${e.server}`)).toEqual(['a@a', 'a@b', 'z@b']);
	});
	it('skips servers without a catalog', () => {
		expect(catalogEntries([server('a', undefined), server('b', [])])).toEqual([]);
	});
});

describe('filterCatalog', () => {
	const all = catalogEntries([server('s1', [{ name: 'm1' }, { name: 'm2' }]), server('s2', [{ name: 'm1' }])]);
	it('returns everything with no filter', () => expect(filterCatalog(all, '', '')).toHaveLength(3));
	it('filters by model', () => expect(filterCatalog(all, 'm1', '')).toHaveLength(2));
	it('filters by model and server', () => expect(filterCatalog(all, 'm1', 's2')).toHaveLength(1));
	it('returns nothing for an unknown model', () => expect(filterCatalog(all, 'nope', '')).toEqual([]));
});

describe('labels', () => {
	it('formats context', () => {
		expect(contextLabel(32768)).toBe('33k tokens');
		expect(contextLabel(512)).toBe('512 tokens');
		expect(contextLabel(undefined)).toBe('');
		expect(contextLabel(-1)).toBe('');
	});
	it('formats parameters', () => {
		expect(parameterLabel(32e9)).toBe('32B');
		expect(parameterLabel(350e6)).toBe('350M');
		expect(parameterLabel(0)).toBe('');
	});
});
