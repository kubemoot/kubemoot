import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import ModelServerCard from './ModelServerCard.svelte';

const server = { apiVersion: 'kubemoot.ai/v1alpha1', kind: 'ModelServer', metadata: { name: 'vllm-rig1', namespace: 'kubemoot' }, spec: { type: 'vllm' }, status: { ready: true, phase: 'Ready', capacity: { loadedModels: [{ name: 'qwen3:14b' }] } } } as never;

describe('ModelServerCard accelerator', () => {
	it('shows the accelerator, its node and busy and memory bars', () => {
		const { container } = render(ModelServerCard, {
			server,
			accelerator: { node: 'rig1', label: 'RTX 4090 24GB', busy: { label: 'Busy', text: '', fraction: 0.3, level: 'normal' }, memory: { label: 'Memory', text: '1 GiB of 2 GiB', fraction: 0.5, level: 'normal' } }
		});
		expect(screen.getByTestId('server-accelerator').textContent).toContain('RTX 4090 24GB');
		expect(container.textContent).toContain('rig1');
		expect(container.querySelectorAll('[role="meter"]')).toHaveLength(2);
	});

	it('says the accelerator is not available and draws no bars when its node is offline', () => {
		const { container } = render(ModelServerCard, { server, accelerator: { node: 'rig3', label: 'RTX 4090 24GB', unavailable: 'Not available to Kubemoot: node offline' } });
		expect(container.textContent).toContain('Not available to Kubemoot: node offline');
		expect(container.querySelector('[role="meter"]')).toBeNull();
	});

	it('shows no accelerator lines when the page could not tell', () => {
		render(ModelServerCard, { server });
		expect(screen.queryByTestId('server-accelerator')).toBeNull();
	});
});
