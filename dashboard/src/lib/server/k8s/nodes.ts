import { getCoreApi } from './client.js';
import type { K8sNode, NodeWithGPU } from '#lib/types/k8s.js';
import { compareCodeUnits, describeError } from '#lib/text-utils.js';
import { extractGPUInfo } from './gpu-info.js';
import { listCrews } from './kubemoot-crds.js';
import type { Crew } from '#lib/types/kubemoot.js';
import { crewDisplayName, type CrewEntry, type CrewNameSource } from '#lib/crew-display-name.js';

export async function listNodes(): Promise<NodeWithGPU[]> {
	const api = getCoreApi();
	const nodeList = await api.listNode();

	return (nodeList.items as unknown as K8sNode[]).map((node) => ({
		...node,
		gpu: extractGPUInfo(node)
	}));
}

export async function getNode(name: string): Promise<NodeWithGPU> {
	const api = getCoreApi();
	const node = await api.readNode({ name }) as unknown as K8sNode;

	return {
		...node,
		gpu: extractGPUInfo(node)
	};
}

export async function listNamespaces(): Promise<string[]> {
	const api = getCoreApi();
	const nsList = await api.listNamespace();

	return nsList.items
		.map((ns: { metadata?: { name?: string } }) => ns.metadata?.name)
		.filter((name: string | undefined): name is string => !!name)
		.sort(compareCodeUnits);
}

/** A crew, the namespace it's deployed to, and the name people read. */
export type CrewNamespace = CrewEntry;

interface NamespaceMeta {
	metadata?: { name?: string; labels?: Record<string, string> };
}

const crewKey = (namespace: string, crew: string) => `${namespace}/${crew}`;

// crewNamespaceEntries pairs each `kubemoot.ai/crew`-labeled namespace with its
// crew and that Crew CR's display name (the technical name when the CR is not
// found or has none), sorted by display name, then technical name, then namespace.
export function crewNamespaceEntries(
	namespaces: readonly NamespaceMeta[],
	crews: readonly CrewNameSource[]
): CrewEntry[] {
	const crByKey = new Map<string, CrewNameSource>();
	for (const cr of crews) {
		const name = cr.metadata?.name;
		const ns = cr.metadata?.namespace;
		if (name && ns) crByKey.set(crewKey(ns, name), cr);
	}
	return namespaces
		.map((ns) => {
			const namespace = ns.metadata?.name ?? '';
			const crew = ns.metadata?.labels?.['kubemoot.ai/crew'] ?? '';
			const cr = crByKey.get(crewKey(namespace, crew)) ?? { metadata: { name: crew } };
			return { namespace, crew, displayName: crewDisplayName(cr) };
		})
		.filter((e) => e.namespace !== '' && e.crew !== '')
		.sort(
			(a, b) =>
				compareCodeUnits(a.displayName, b.displayName) ||
				compareCodeUnits(a.crew, b.crew) ||
				compareCodeUnits(a.namespace, b.namespace)
		);
}

// listCrewNamespaces returns only the namespaces that host a Kubemoot crew -
// those carrying the `kubemoot.ai/crew` label - paired with the crew name and
// display name. The dashboard's top-bar selector scopes pages by crew, so
// non-crew namespaces (kube-system, flux-system, the shared kubemoot
// control-plane ns, ...) are noise there and excluded. "All crews" (no
// selection) still shows everything. When the Crew CRs cannot be listed, every
// entry shows its technical name.
export async function listCrewNamespaces(): Promise<CrewNamespace[]> {
	const api = getCoreApi();
	const [nsList, crewList] = await Promise.all([
		api.listNamespace(),
		listCrews('').catch((err: unknown) => {
			console.warn(`Crew CRs not listed, showing technical crew names: ${describeError(err)}`);
			return { items: [] as Crew[] };
		})
	]);
	return crewNamespaceEntries(nsList.items, crewList.items);
}
