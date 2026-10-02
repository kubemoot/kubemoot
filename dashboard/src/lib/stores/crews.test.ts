import { get } from 'svelte/store';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { CrewEntry } from '$lib/crew-display-name';
import { crewDirectory, loadCrewDirectory } from './crews';

const pilot: CrewEntry = { namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' };

function respond(body: unknown): typeof fetch {
	return vi.fn(async () => ({ json: async () => body }) as unknown as Response);
}

describe('crewDirectory', () => {
	beforeEach(() => crewDirectory.set([]));

	it('starts empty and holds what is set', () => {
		expect(get(crewDirectory)).toEqual([]);
		crewDirectory.set([pilot]);
		expect(get(crewDirectory)).toEqual([pilot]);
	});
});

describe('loadCrewDirectory', () => {
	beforeEach(() => crewDirectory.set([pilot]));

	it('fills the directory from the endpoint', async () => {
		const fetchFn = respond({ crews: [pilot] });
		crewDirectory.set([]);
		await loadCrewDirectory(fetchFn, '/dashboard/api/namespaces');
		expect(fetchFn).toHaveBeenCalledWith('/dashboard/api/namespaces');
		expect(get(crewDirectory)).toEqual([pilot]);
	});

	it('empties the directory for an error body or an unexpected shape', async () => {
		await loadCrewDirectory(respond({ error: 'Failed to list crews' }), '/api/namespaces');
		expect(get(crewDirectory)).toEqual([]);

		crewDirectory.set([pilot]);
		await loadCrewDirectory(respond({ crews: 'not a list' }), '/api/namespaces');
		expect(get(crewDirectory)).toEqual([]);

		crewDirectory.set([pilot]);
		await loadCrewDirectory(respond(null), '/api/namespaces');
		expect(get(crewDirectory)).toEqual([]);
	});

	it('empties the directory when the request or the body fails', async () => {
		await loadCrewDirectory(vi.fn(async () => Promise.reject(new Error('offline'))), '/api/namespaces');
		expect(get(crewDirectory)).toEqual([]);

		crewDirectory.set([pilot]);
		const badJson = vi.fn(async () => ({ json: async () => Promise.reject(new SyntaxError('bad json')) }) as unknown as Response);
		await loadCrewDirectory(badJson, '/api/namespaces');
		expect(get(crewDirectory)).toEqual([]);
	});
});
