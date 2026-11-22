import { preferred } from './accelerator-capacity';
import type { AcceleratorInfo, NodeWithAccelerator } from '#lib/types/k8s.js';

/**
 * The accelerators a node has: `accelerator`, else the deprecated `gpu` that an older
 * dashboard server sends. Undefined when the node reports none.
 */
export function nodeAccelerator(node: Pick<NodeWithAccelerator, 'accelerator' | 'gpu'>): AcceleratorInfo | undefined {
	return preferred(node.accelerator, node.gpu);
}
