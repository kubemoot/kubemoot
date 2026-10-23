import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, it, expect, vi } from 'vitest';
import YamlView from './YamlView.svelte';

const resource = { apiVersion: 'kubemoot.ai/v1alpha1', kind: 'PromptModule', metadata: { name: 'mtg-advisory', managedFields: [{}] }, spec: { text: 'hi' } };

describe('YamlView', () => {
	it('renders nothing before the resource has loaded', () => {
		const { container } = render(YamlView, { props: { resource: null } });
		expect(container.querySelector('details')).toBeNull();
	});

	it('shows the resource as YAML without managedFields', () => {
		const { container } = render(YamlView, { props: { resource } });
		const text = container.querySelector('pre')?.textContent ?? '';
		expect(text).toContain('kind: PromptModule');
		expect(text).toContain('name: mtg-advisory');
		expect(text).not.toContain('managedFields');
	});

	it('copies the YAML to the clipboard', async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(YamlView, { props: { resource } });
		await fireEvent.click(screen.getByRole('button', { name: /copy/i }));
		expect(writeText).toHaveBeenCalledWith(expect.stringContaining('kind: PromptModule'));
	});
});
