// Which nodes the Nodes page lists: all of them, or only the worker, accelerator or control plane
// nodes. A node is a control plane node when it carries the control-plane role label; it is an
// accelerator node when it reports an allocatable accelerator device of any kind (GPU, TPU, NPU),
// whatever its vendor; every node that is not a control plane node is a worker. The choice lives
// in the URL (?role=), which wins, and is remembered per viewer in this browser.

import { nodeAccelerator } from '#lib/node-accelerator.js';
import { readItem, writeItem } from '#lib/safe-storage.js';
import type { NodeWithAccelerator } from '#lib/types/k8s.js';

export type NodeRole = 'all' | 'worker' | 'accelerator' | 'control-plane';

export const ROLE_OPTIONS: readonly { id: NodeRole; label: string }[] = [
	{ id: 'all', label: 'All' },
	{ id: 'worker', label: 'Worker nodes' },
	{ id: 'accelerator', label: 'Accelerator nodes' },
	{ id: 'control-plane', label: 'Control plane nodes' }
];

export const DEFAULT_ROLE: NodeRole = 'all';

export const CONTROL_PLANE_LABEL = 'node-role.kubernetes.io/control-plane';

/** The node carries the control-plane role label (its value is usually empty). */
export function isControlPlane(node: Pick<NodeWithAccelerator, 'metadata'>): boolean {
	return node.metadata.labels !== undefined && CONTROL_PLANE_LABEL in node.metadata.labels;
}

type RoleNode = Pick<NodeWithAccelerator, 'metadata' | 'accelerator' | 'gpu'>;

const MATCHES: Record<NodeRole, (node: RoleNode) => boolean> = {
	all: () => true,
	worker: (node) => !isControlPlane(node),
	accelerator: (node) => nodeAccelerator(node)?.present === true,
	'control-plane': isControlPlane
};

export function matchesRole(node: RoleNode, role: NodeRole): boolean {
	return MATCHES[role](node);
}

/** The role named by a ?role= value or a stored one; null for anything else. */
export function parseRole(value: string | null | undefined): NodeRole | null {
	return ROLE_OPTIONS.find((o) => o.id === value)?.id ?? null;
}

/** The role to show: the URL's, else the one remembered, else All. */
export function chosenRole(fromUrl: string | null, remembered: NodeRole | null): NodeRole {
	return parseRole(fromUrl) ?? remembered ?? DEFAULT_ROLE;
}

/** The query string that opens the Nodes page on a role. A link always names its role, so it never inherits a remembered one. */
export function roleSearch(role: NodeRole): string {
	return `?role=${role}`;
}

const STORAGE_KEY = 'kubemoot-dashboard-nodes-role';

/** The remembered role, or null when none is kept or storage is unavailable. */
export function loadRole(): NodeRole | null {
	return parseRole(readItem(STORAGE_KEY));
}

/** Remembers the role; does nothing when storage is unavailable or full. */
export function saveRole(role: NodeRole): void {
	writeItem(STORAGE_KEY, role);
}
