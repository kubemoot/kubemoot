import { describe, expect, it } from 'vitest';
import { buildGpuDisplayMap, endpointNamespace, gpuDisplayName } from './gpu-display';

describe('gpuDisplayName', () => {
	it('drops the NVIDIA GeForce prefix and trims', () => {
		expect(gpuDisplayName('NVIDIA GeForce RTX 5090 ')).toBe('RTX 5090');
		expect(gpuDisplayName('AMD Radeon Pro W7900')).toBe('AMD Radeon Pro W7900');
	});
});

describe('endpointNamespace', () => {
	it('takes the second host label as the namespace', () => {
		expect(endpointNamespace('https://ollama.ollama-rig0:11434/api')).toBe('ollama-rig0');
		expect(endpointNamespace('ollama.ollama-gpu.svc.cluster.local')).toBe('ollama-gpu');
	});

	it('uses the whole host when it has a single label', () => {
		expect(endpointNamespace('https://ollama:11434')).toBe('ollama');
		expect(endpointNamespace('')).toBe('');
	});
});

describe('buildGpuDisplayMap', () => {
	it('keys the display name by provider name and endpoint namespace', () => {
		expect(
			buildGpuDisplayMap([
				{
					metadata: { name: 'ollama-gpu' },
					spec: { endpoint: 'https://ollama.ollama-rig1:11434' },
					status: { capacity: { gpuModel: 'NVIDIA GeForce RTX 5090' } }
				},
				{ metadata: { name: 'no-gpu' }, status: { capacity: {} } },
				{ status: { capacity: { gpuModel: 'Tesla T4' } } }
			])
		).toEqual({ 'ollama-gpu': 'RTX 5090', 'ollama-rig1': 'RTX 5090' });
	});

	it('is empty for no providers', () => {
		expect(buildGpuDisplayMap([])).toEqual({});
	});
});
