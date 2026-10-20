// Maps the server ids that agents publish in span metadata to the GPU model the
// operator discovered on each ModelServer, for display only.

interface ServerLike {
	metadata?: { name?: string };
	spec?: { endpoint?: string };
	status?: { capacity?: { gpuModel?: string } };
}

/** The compact GPU name: "NVIDIA GeForce RTX 5090" becomes "RTX 5090". */
export function gpuDisplayName(gpuModel: string): string {
	return gpuModel.replace(/^NVIDIA GeForce /, '').trim();
}

/** The namespace part of an endpoint host: "http://ollama.ollama-rig0:11434" gives "ollama-rig0". */
export function endpointNamespace(endpoint: string): string {
	const host = endpoint
		.replace(/^[a-z]+:\/\//, '')
		.split('/')[0]
		.split(':')[0];
	const parts = host.split('.');
	return parts.length >= 2 ? parts[1] : parts[0];
}

/** The ids a server is published under: its CR name and its endpoint namespace. */
function serverIds(mp: ServerLike): string[] {
	const endpoint = mp.spec?.endpoint;
	const ids = [mp.metadata?.name, endpoint ? endpointNamespace(endpoint) : undefined];
	return ids.filter((id): id is string => !!id);
}

/**
 * GPU display name keyed by every id a server is published under: its CR name and
 * its endpoint namespace. Servers without a discovered GPU model are left out.
 */
export function buildGpuDisplayMap(servers: ServerLike[]): Record<string, string> {
	const map: Record<string, string> = {};
	for (const mp of servers) {
		const gpuModel = mp?.status?.capacity?.gpuModel;
		if (!gpuModel) continue;
		const display = gpuDisplayName(gpuModel);
		for (const id of serverIds(mp)) map[id] = display;
	}
	return map;
}
