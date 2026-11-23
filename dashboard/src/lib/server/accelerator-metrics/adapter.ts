import type { AcceleratorUse, AcceleratorUseByNode } from '#lib/accelerator-use.js';

/** One series of an instant query: its labels and current value. */
export interface Sample {
	labels: Record<string, string>;
	value: number;
}

/**
 * What one vendor's exporter must say to feed the generic reading. Utilization is a percent, memory is
 * in MiB, and every series is one device. Add an adapter (AMD, others) beside the DCGM one; no page changes.
 */
export interface ExporterAdapter {
	name: string;
	queries: { utilizationPercent: string; memoryUsedMiB: string; memoryFreeMiB: string };
	/** The node a series is about, from its labels; undefined when it names none. */
	nodeOf(labels: Record<string, string>): string | undefined;
	/** A key that tells devices on one node apart. */
	deviceOf(labels: Record<string, string>): string;
}

export interface Measures {
	utilization: Sample[];
	memoryUsed: Sample[];
	memoryFree: Sample[];
}

type Device = { busy?: number; used?: number; free?: number };

function devicesByNode(adapter: ExporterAdapter, measures: Measures): Map<string, Map<string, Device>> {
	const nodes = new Map<string, Map<string, Device>>();
	const put = (samples: Sample[], set: (d: Device, v: number) => void) => {
		for (const s of samples) {
			const node = adapter.nodeOf(s.labels);
			if (!node || !Number.isFinite(s.value)) continue;
			const devices = nodes.get(node) ?? new Map<string, Device>();
			nodes.set(node, devices);
			const key = adapter.deviceOf(s.labels);
			const device = devices.get(key) ?? {};
			devices.set(key, device);
			set(device, s.value);
		}
	};
	put(measures.utilization, (d, v) => (d.busy = Math.min(1, Math.max(0, v / 100))));
	put(measures.memoryUsed, (d, v) => (d.used = v));
	put(measures.memoryFree, (d, v) => (d.free = v));
	return nodes;
}

function sumOf(list: (number | undefined)[]): number | undefined {
	const known = list.filter((n): n is number => n !== undefined);
	return known.length === list.length && known.length > 0 ? known.reduce((a, b) => a + b, 0) : undefined;
}

function useOf(devices: Device[]): AcceleratorUse {
	const busy = devices.map((d) => d.busy).filter((b): b is number => b !== undefined);
	const used = sumOf(devices.map((d) => d.used));
	const free = sumOf(devices.map((d) => d.free));
	return {
		devices: devices.length,
		busy: busy.length === devices.length ? busy.reduce((a, b) => a + b, 0) / busy.length : undefined,
		memoryUsedMiB: used,
		memoryTotalMiB: used !== undefined && free !== undefined ? used + free : undefined
	};
}

/** The generic use per node from one exporter's samples: busy averaged over devices, memory summed. */
export function toUse(adapter: ExporterAdapter, measures: Measures): AcceleratorUseByNode {
	const result: AcceleratorUseByNode = {};
	for (const [node, devices] of devicesByNode(adapter, measures)) result[node] = useOf([...devices.values()]);
	return result;
}
