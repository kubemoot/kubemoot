import type { AcceleratorUseResponse } from '../accelerator-use.js';
import type { PodCount, PodRef, Reading } from '../reading.js';
import type { NodeWithAccelerator } from '../types/k8s.js';
import type { ModelServer } from '../types/kubemoot.js';

/** The measured accelerator use; null when the endpoint could not be read. */
export async function fetchAcceleratorUse(url: string): Promise<AcceleratorUseResponse | null> {
	try {
		const res = await fetch(url);
		return res.ok ? ((await res.json()) as AcceleratorUseResponse) : null;
	} catch {
		return null;
	}
}

/** Every model server the account can read, in all namespaces; undefined when they could not be read. */
export async function fetchModelServers(url: string): Promise<ModelServer[] | undefined> {
	try {
		const res = await fetch(`${url}?namespace=`);
		if (!res.ok) return undefined;
		return ((await res.json()) as { items?: ModelServer[] }).items ?? [];
	} catch {
		return undefined;
	}
}

/** What a page needs to say which accelerator a model server runs on and how it is working. */
export interface AcceleratorView {
	nodes: NodeWithAccelerator[];
	holdersByNode: Record<string, PodRef[]>;
	use: AcceleratorUseResponse | null;
}

async function getJson<T>(url: string): Promise<T | undefined> {
	try {
		const res = await fetch(url);
		return res.ok ? ((await res.json()) as T) : undefined;
	} catch {
		return undefined;
	}
}

/** The nodes, the pods that hold accelerators on each, and the measured use; each part is empty when it could not be read. */
export async function fetchAcceleratorView(urls: { nodes: string; usage: string; accelerators: string }): Promise<AcceleratorView> {
	const [nodes, usage, use] = await Promise.all([
		getJson<{ nodes?: NodeWithAccelerator[] }>(urls.nodes),
		getJson<{ pods?: Reading<Record<string, PodCount>> }>(urls.usage),
		fetchAcceleratorUse(urls.accelerators)
	]);
	const pods = usage?.pods?.available ? usage.pods.data : {};
	return { nodes: nodes?.nodes ?? [], holdersByNode: Object.fromEntries(Object.entries(pods).map(([node, count]) => [node, count.holders ?? []])), use };
}
