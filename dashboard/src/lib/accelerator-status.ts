// The accelerator section of a node: is the accelerator available to Kubemoot, what is using it, and is it
// working right now. Allocation to a pod says nothing about work: a model server holds its device for good.

import { acceleratorDisplayName, endpointNamespace } from './accelerator-display.js';
import { nodeAccelerator } from './node-accelerator.js';
import { gib, isNodeReady } from './node-usage.js';
import { bar, type TileBar } from './usage-bar.js';
import type { AcceleratorUse, AcceleratorUseByNode } from './accelerator-use.js';
import type { PodRef } from './reading.js';
import type { NodeWithAccelerator } from './types/k8s.js';
import type { ModelServer } from './types/kubemoot.js';

/** What holds the accelerator: a Kubemoot model server with its models, some other pod, or nothing. */
export type Serving =
	| { kind: 'server'; name: string; namespace: string; models: string[] }
	| { kind: 'pod'; pod: string }
	| { kind: 'free' };

export interface AcceleratorStatus {
	/** "RTX 5090 32GB", with the count when there are several. */
	label: string;
	/** Why Kubemoot cannot use it, when it cannot; no bars are drawn then. */
	unavailable?: string;
	/** Undefined when the pods could not be read, so the page says nothing rather than guess. */
	serving?: Serving;
	/** How busy it is now; undefined when no metrics source reports it. */
	busy?: TileBar;
	/** Accelerator memory in use; undefined when no metrics source reports it. */
	memory?: TileBar;
}

export const OFFLINE_TEXT = 'Not available to Kubemoot: node offline';
export const PODS_OFFLINE_TEXT = 'unknown while the node is offline';

/** Pod names a workload gives its pods: Deployment (name-hash-suffix), ReplicaSet or DaemonSet (name-suffix), StatefulSet (name-0). */
const POD_SUFFIX = /^(?:[a-z0-9]{6,10}-)?[a-z0-9]{5}$|^\d+$/;

/** The Service name and namespace a model server answers on, from its serviceRef or its endpoint host. */
function serviceOf(server: ModelServer): { name: string; namespace: string } | undefined {
	const ref = server.spec.serviceRef;
	if (ref?.name) return { name: ref.name, namespace: ref.namespace ?? server.metadata.namespace ?? '' };
	const endpoint = server.spec.endpoint;
	if (!endpoint) return undefined;
	const host = endpoint.replace(/^[a-z]+:\/\//, '').split('/')[0].split(':')[0];
	const [name] = host.split('.');
	return name ? { name, namespace: endpointNamespace(endpoint) } : undefined;
}

/** Whether a pod is one of the pods behind the model server's Service. */
export function podServes(pod: PodRef, server: ModelServer): boolean {
	const service = serviceOf(server);
	if (service?.namespace !== pod.namespace || !pod.name.startsWith(service.name + '-')) return false;
	return POD_SUFFIX.test(pod.name.slice(service.name.length + 1));
}

/** The model server whose Service the pod is behind, or whose status names the node it runs on. */
export function serverOfPod(pod: PodRef, node: string, servers: readonly ModelServer[]): ModelServer | undefined {
	return servers.find((s) => podServes(pod, s)) ?? servers.find((s) => s.status?.capacity?.nodeName === node);
}

/** What holds the node's accelerator, from the pods that hold a device and the model servers. */
export function servingOn(node: string, holders: readonly PodRef[], servers: readonly ModelServer[]): Serving {
	for (const pod of holders) {
		const server = serverOfPod(pod, node, servers);
		if (server) {
			const models = (server.status?.capacity?.loadedModels ?? []).map((m) => m.name);
			return { kind: 'server', name: server.metadata.name, namespace: server.metadata.namespace ?? '', models };
		}
	}
	return holders.length > 0 ? { kind: 'pod', pod: holders[0].name } : { kind: 'free' };
}

/** The node a model server runs on, from the pods that hold accelerators on each node. */
export function nodeOfServer(server: ModelServer, holdersByNode: Readonly<Record<string, readonly PodRef[]>>): string | undefined {
	const named = server.status?.capacity?.nodeName;
	if (named) return named;
	return Object.keys(holdersByNode).find((node) => holdersByNode[node].some((pod) => podServes(pod, server)));
}

const mib = (n: number) => gib(n * 1024 ** 2);

/** The bars for a node's measured use. Full is expected (a server reserves its memory), so they never warn. */
export function useBars(use: AcceleratorUse | undefined): Pick<AcceleratorStatus, 'busy' | 'memory'> {
	if (!use) return {};
	const busy = use.busy === undefined ? undefined : bar('Busy', '', use.busy);
	const total = use.memoryTotalMiB;
	const used = use.memoryUsedMiB;
	const memory = used !== undefined && total !== undefined && total > 0 ? bar('Memory', `${mib(used)} of ${mib(total)}`, used / total) : undefined;
	return { busy: busy && { ...busy, level: 'normal' }, memory: memory && { ...memory, level: 'normal' } };
}

/** "RTX 5090 32GB" from the labels the node carries; with the count when it has several devices. */
function labelOf(node: NodeWithAccelerator): string | undefined {
	const accelerator = nodeAccelerator(node);
	if (!accelerator?.present) return undefined;
	const count = accelerator.count ?? 1;
	const type = accelerator.type ? acceleratorDisplayName(accelerator.type.replaceAll('-', ' ')) : 'Accelerator';
	return [type, count > 1 ? `x${count}` : undefined, accelerator.memory].filter(Boolean).join(' ');
}

export interface StatusInput {
	node: NodeWithAccelerator;
	/** Pods that hold an accelerator device on this node; undefined when the pods could not be read. */
	holders?: readonly PodRef[];
	/** Undefined while the model servers load or when they could not be read. */
	servers?: readonly ModelServer[];
	/** Measured use for every node; undefined when no metrics source is set or it could not be read. */
	use?: AcceleratorUseByNode;
}

/** The accelerator section for a node; undefined when the node has no accelerator. */
export function acceleratorStatus({ node, holders, servers, use }: StatusInput): AcceleratorStatus | undefined {
	const label = labelOf(node);
	if (!label) return undefined;
	if (!isNodeReady(node)) return { label, unavailable: OFFLINE_TEXT };
	const serving = holders && servers ? servingOn(node.metadata.name, holders, servers) : undefined;
	return { label, serving, ...useBars(use?.[node.metadata.name]) };
}

/** The accelerators of every Ready node measured together: busy averaged over devices, memory summed. Undefined when none was measured. */
export function combinedUse(nodes: readonly NodeWithAccelerator[], use: AcceleratorUseByNode | undefined): AcceleratorUse | undefined {
	if (!use) return undefined;
	const measured = nodes.filter((n) => nodeAccelerator(n)?.present && isNodeReady(n)).flatMap((n) => (use[n.metadata.name] ? [use[n.metadata.name]] : []));
	if (measured.length === 0) return undefined;
	const devices = measured.reduce((sum, u) => sum + u.devices, 0);
	const busy = measured.every((u) => u.busy !== undefined) && devices > 0 ? measured.reduce((sum, u) => sum + (u.busy ?? 0) * u.devices, 0) / devices : undefined;
	const memory = measured.every((u) => u.memoryUsedMiB !== undefined && u.memoryTotalMiB !== undefined);
	return {
		devices,
		busy,
		memoryUsedMiB: memory ? measured.reduce((sum, u) => sum + (u.memoryUsedMiB ?? 0), 0) : undefined,
		memoryTotalMiB: memory ? measured.reduce((sum, u) => sum + (u.memoryTotalMiB ?? 0), 0) : undefined
	};
}

/** The accelerator a model server runs on: where it is, whether Kubemoot can use it, and how it is working. */
export interface ServerAccelerator extends Pick<AcceleratorStatus, 'label' | 'unavailable' | 'busy' | 'memory'> {
	node: string;
}

/** The accelerator behind a model server; undefined when its node cannot be told or has no accelerator. */
export function serverAccelerator(
	server: ModelServer,
	nodes: readonly NodeWithAccelerator[],
	holdersByNode: Readonly<Record<string, readonly PodRef[]>>,
	use: AcceleratorUseByNode | undefined
): ServerAccelerator | undefined {
	const name = nodeOfServer(server, holdersByNode);
	const node = nodes.find((n) => n.metadata.name === name);
	const status = node && acceleratorStatus({ node, use });
	if (!node || !status) return undefined;
	return { node: node.metadata.name, label: status.label, unavailable: status.unavailable, busy: status.busy, memory: status.memory };
}
