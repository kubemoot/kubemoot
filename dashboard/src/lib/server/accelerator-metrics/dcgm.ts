import type { ExporterAdapter } from './adapter.js';

/** Labels NVIDIA's DCGM exporter (and Prometheus relabeling of it) uses for the node a device is on. */
const NODE_LABELS = ['Hostname', 'hostname', 'node', 'kubernetes_node'];

/** NVIDIA DCGM exporter: DCGM_FI_DEV_GPU_UTIL (percent), DCGM_FI_DEV_FB_USED and DCGM_FI_DEV_FB_FREE (MiB). */
export const dcgmAdapter: ExporterAdapter = {
	name: 'dcgm',
	queries: {
		utilizationPercent: 'DCGM_FI_DEV_GPU_UTIL',
		memoryUsedMiB: 'DCGM_FI_DEV_FB_USED',
		memoryFreeMiB: 'DCGM_FI_DEV_FB_FREE'
	},
	nodeOf(labels) {
		for (const key of NODE_LABELS) if (labels[key]) return labels[key];
		return undefined;
	},
	deviceOf(labels) {
		return labels.UUID || `${labels.gpu ?? ''}/${labels.pci_bus_id ?? ''}`;
	}
};
