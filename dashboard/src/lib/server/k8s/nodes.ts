import { getCoreApi } from './client.js';
import type { K8sNode, NodeWithAccelerator } from '#lib/types/k8s.js';
import { compareCodeUnits } from '#lib/text-utils.js';
import { extractAcceleratorInfo } from './accelerator-info.js';
import { listCrews } from './kubemoot-crds.js';
import { crewDisplayName, type CrewEntry, type CrewNameSource } from '#lib/crew-display-name.js';

/**
 * The node with its accelerator summary. `gpu` is the deprecated old name of `accelerator`,
 * sent for one release for readers outside the dashboard; remove it in the next.
 */
export function withAccelerator(node: K8sNode): NodeWithAccelerator {
	const accelerator = extractAcceleratorInfo(node);
	return { ...node, accelerator, gpu: accelerator };
}

export async function listNodes(): Promise<NodeWithAccelerator[]> {
	const api = getCoreApi();
	const nodeList = await api.listNode();

	return (nodeList.items as unknown as K8sNode[]).map(withAccelerator);
}

export async function getNode(name: string): Promise<NodeWithAccelerator> {
	const api = getCoreApi();
	const node = await api.readNode({ name }) as unknown as K8sNode;

	return withAccelerator(node);
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

/**
 * The crews the person can see as Crew resources (the Crews page's own source), each with the
 * name people read, sorted by display name, then technical name, then namespace. A Crew without
 * a name or namespace is dropped.
 */
export function crewEntries(crews: readonly CrewNameSource[]): CrewEntry[] {
	return crews
		.map((cr) => ({
			namespace: cr.metadata?.namespace ?? '',
			crew: cr.metadata?.name ?? '',
			displayName: crewDisplayName(cr)
		}))
		.filter((e) => e.namespace !== '' && e.crew !== '')
		.sort(
			(a, b) =>
				compareCodeUnits(a.displayName, b.displayName) ||
				compareCodeUnits(a.crew, b.crew) ||
				compareCodeUnits(a.namespace, b.namespace)
		);
}

/**
 * The crews for the top-bar selector: the Crew resources the person may list, the same ones the
 * Crews page shows. A namespace that merely carries the crew label, with no Crew in it, is not
 * offered: it has no gateway to ask.
 */
export async function listCrewNamespaces(): Promise<CrewNamespace[]> {
	const crews = await listCrews('');
	return crewEntries(crews.items);
}
