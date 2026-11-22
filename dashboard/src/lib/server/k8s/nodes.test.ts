import { beforeEach, describe, expect, it, vi } from 'vitest';

const listNamespace = vi.fn();
const listCrews = vi.fn();

vi.mock('./client.js', () => ({ getCoreApi: () => ({ listNamespace }), getServiceAccountCoreApi: () => ({ listNamespace }) }));
vi.mock('./kubemoot-crds.js', () => ({ listCrews: (ns: string) => listCrews(ns) }));

const { crewEntries, listCrewNamespaces, withAccelerator } = await import('./nodes');

function ns(name: string | undefined, crew?: string) {
	const labels: Record<string, string> = crew === undefined ? {} : { 'kubemoot.ai/crew': crew };
	return { metadata: { name, labels } };
}

function crewCr(namespace: string | undefined, name: string | undefined, displayName?: string) {
	return {
		metadata: {
			name,
			namespace,
			annotations: displayName === undefined ? undefined : { 'kubemoot.ai/display-name': displayName }
		}
	};
}

describe('withAccelerator', () => {
	it('sends the accelerator summary under accelerator and the deprecated gpu', () => {
		const node = { metadata: { name: 'n1' }, status: { capacity: { 'nvidia.com/gpu': '2' } } };
		const out = withAccelerator(node as never);
		expect(out.accelerator).toMatchObject({ present: true, count: 2 });
		expect(out.gpu).toEqual(out.accelerator);
		expect(out.metadata.name).toBe('n1');
	});

	it('reports no accelerator on both names for a node without one', () => {
		const out = withAccelerator({ metadata: { name: 'cpu-only' }, status: { capacity: { cpu: '8' } } } as never);
		expect(out.accelerator).toEqual({ present: false });
		expect(out.gpu).toEqual({ present: false });
	});
});

describe('crewEntries', () => {
	it('lists each Crew resource with its display name, or its technical name when it has none', () => {
		expect(
			crewEntries([crewCr('guide', 'homelab-health-guide', 'Homelab Health Guide'), crewCr('pilot', 'homelab-pilot'), crewCr('blank', 'x', '   ')])
		).toEqual([
			{ namespace: 'guide', crew: 'homelab-health-guide', displayName: 'Homelab Health Guide' },
			{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'homelab-pilot' },
			{ namespace: 'blank', crew: 'x', displayName: 'x' }
		]);
	});

	it('drops a Crew with no name or namespace, and one with no metadata', () => {
		expect(crewEntries([{}, { metadata: null }, crewCr(undefined, 'a'), crewCr('ns', undefined), crewCr('pilot', 'homelab-pilot')])).toEqual([
			{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'homelab-pilot' }
		]);
	});

	it('sorts by display name, then technical name, then namespace, by code unit', () => {
		const entries = crewEntries([crewCr('b', 'zeta'), crewCr('a', 'zeta'), crewCr('c', 'alpha', 'Zulu'), crewCr('d', 'mid', 'Alpha Team')]);
		expect(entries.map((e) => `${e.namespace}/${e.crew}`)).toEqual(['d/mid', 'c/alpha', 'a/zeta', 'b/zeta']);
	});

	it('is empty for empty input', () => {
		expect(crewEntries([])).toEqual([]);
	});
});

describe('listCrewNamespaces', () => {
	beforeEach(() => {
		listCrews.mockReset();
	});

	it('lists the Crew resources across all namespaces, never the namespaces', async () => {
		listCrews.mockResolvedValue({ items: [crewCr('pilot', 'homelab-pilot', 'Homelab Pilot')] });
		await expect(listCrewNamespaces()).resolves.toEqual([{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' }]);
		expect(listCrews).toHaveBeenCalledWith('');
		expect(listNamespace).not.toHaveBeenCalled();
	});

	it('does not offer a labeled namespace that holds no Crew', async () => {
		listNamespace.mockResolvedValue({ items: [ns('default', 'demo'), ns('workshop', 'workshop')] });
		listCrews.mockResolvedValue({ items: [crewCr('pilot', 'homelab-pilot')] });
		const entries = await listCrewNamespaces();
		expect(entries.map((e) => e.namespace)).toEqual(['pilot']);
	});

	it('fails when the Crew resources cannot be listed', async () => {
		listCrews.mockRejectedValue(new Error('forbidden'));
		await expect(listCrewNamespaces()).rejects.toThrow('forbidden');
	});
});
