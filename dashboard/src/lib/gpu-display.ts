// Maps the provider ids that agents publish in span metadata to the GPU model the
// operator discovered on each ModelProvider, for display only.

interface ProviderLike {
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

/** The ids a provider is published under: its CR name and its endpoint namespace. */
function providerIds(mp: ProviderLike): string[] {
	const endpoint = mp.spec?.endpoint;
	const ids = [mp.metadata?.name, endpoint ? endpointNamespace(endpoint) : undefined];
	return ids.filter((id): id is string => !!id);
}

/**
 * GPU display name keyed by every id a provider is published under: its CR name and
 * its endpoint namespace. Providers without a discovered GPU model are left out.
 */
export function buildGpuDisplayMap(providers: ProviderLike[]): Record<string, string> {
	const map: Record<string, string> = {};
	for (const mp of providers) {
		const gpuModel = mp?.status?.capacity?.gpuModel;
		if (!gpuModel) continue;
		const display = gpuDisplayName(gpuModel);
		for (const id of providerIds(mp)) map[id] = display;
	}
	return map;
}
