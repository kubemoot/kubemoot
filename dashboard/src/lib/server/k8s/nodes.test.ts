import { beforeEach, describe, expect, it, vi } from 'vitest';

const listNamespace = vi.fn();
const listCrews = vi.fn();

vi.mock('./client.js', () => ({ getCoreApi: () => ({ listNamespace }) }));
vi.mock('./kubemoot-crds.js', () => ({ listCrews: (ns: string) => listCrews(ns) }));

const { crewNamespaceEntries, listCrewNamespaces } = await import('./nodes');

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

describe('crewNamespaceEntries', () => {
	it('pairs each crew namespace with its Crew CR display name', () => {
		const entries = crewNamespaceEntries(
			[ns('guide', 'homelab-health-guide'), ns('pilot', 'homelab-pilot')],
			[crewCr('guide', 'homelab-health-guide', 'Homelab Health Guide'), crewCr('pilot', 'homelab-pilot')]
		);
		expect(entries).toEqual([
			{ namespace: 'guide', crew: 'homelab-health-guide', displayName: 'Homelab Health Guide' },
			{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'homelab-pilot' }
		]);
	});

	it('uses the technical name when no Crew CR matches the namespace and name', () => {
		const entries = crewNamespaceEntries(
			[ns('pilot', 'homelab-pilot')],
			[crewCr('other', 'homelab-pilot', 'Wrong Namespace'), crewCr('pilot', 'renamed', 'Wrong Name')]
		);
		expect(entries).toEqual([{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'homelab-pilot' }]);
	});

	it('uses the technical name when the annotation is blank', () => {
		const entries = crewNamespaceEntries([ns('pilot', 'homelab-pilot')], [crewCr('pilot', 'homelab-pilot', '   ')]);
		expect(entries[0].displayName).toBe('homelab-pilot');
	});

	it('drops namespaces without a crew label or name, and ignores Crew CRs without metadata', () => {
		const entries = crewNamespaceEntries(
			[ns('kube-system'), ns('blank', ''), ns(undefined, 'orphan'), {}, ns('pilot', 'homelab-pilot')],
			[{}, { metadata: null }, crewCr(undefined, 'homelab-pilot', 'No Namespace')]
		);
		expect(entries).toEqual([{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'homelab-pilot' }]);
	});

	it('sorts by display name, then technical name, then namespace, by code unit', () => {
		const entries = crewNamespaceEntries(
			[ns('b', 'zeta'), ns('a', 'zeta'), ns('c', 'alpha'), ns('d', 'mid')],
			[crewCr('c', 'alpha', 'Zulu'), crewCr('d', 'mid', 'Alpha Team')]
		);
		expect(entries.map((e) => `${e.namespace}/${e.crew}`)).toEqual(['d/mid', 'c/alpha', 'a/zeta', 'b/zeta']);
	});

	it('is empty for empty input', () => {
		expect(crewNamespaceEntries([], [])).toEqual([]);
	});
});

describe('listCrewNamespaces', () => {
	beforeEach(() => {
		listNamespace.mockReset();
		listCrews.mockReset();
	});

	it('lists Crew CRs across all namespaces and joins their display names', async () => {
		listNamespace.mockResolvedValue({ items: [ns('pilot', 'homelab-pilot'), ns('kube-system')] });
		listCrews.mockResolvedValue({ items: [crewCr('pilot', 'homelab-pilot', 'Homelab Pilot')] });
		await expect(listCrewNamespaces()).resolves.toEqual([
			{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' }
		]);
		expect(listCrews).toHaveBeenCalledWith('');
	});

	it('falls back to technical names and warns when the Crew CRs cannot be listed', async () => {
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		listNamespace.mockResolvedValue({ items: [ns('pilot', 'homelab-pilot')] });
		listCrews.mockRejectedValue(new Error('forbidden'));
		await expect(listCrewNamespaces()).resolves.toEqual([
			{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'homelab-pilot' }
		]);
		expect(warn).toHaveBeenCalledTimes(1);
		expect(warn.mock.calls[0][0]).toContain('forbidden');
		warn.mockRestore();
	});

	it('does not warn when the Crew CRs are listed', async () => {
		const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
		listNamespace.mockResolvedValue({ items: [] });
		listCrews.mockResolvedValue({ items: [] });
		await expect(listCrewNamespaces()).resolves.toEqual([]);
		expect(warn).not.toHaveBeenCalled();
		warn.mockRestore();
	});

	it('fails when the namespaces cannot be listed', async () => {
		listNamespace.mockRejectedValue(new Error('unreachable'));
		listCrews.mockResolvedValue({ items: [] });
		await expect(listCrewNamespaces()).rejects.toThrow('unreachable');
	});
});
