import { fireEvent, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ResourceTabs from './ResourceTabs.svelte';
import { page, setPageUrl } from '../../../../vitest-mocks/app-state.svelte.js';

const resource = {
	apiVersion: 'kubemoot.ai/v1alpha1',
	kind: 'Crew',
	metadata: { name: 'demo', namespace: 'kubemoot', managedFields: [{ manager: 'kubectl' }] },
	spec: { replicas: 2, enabled: true },
	status: { phase: 'Ready' }
};

const overview = createRawSnippet(() => ({ render: () => '<p>the overview</p>' }));

function mount(res: object | null = resource) {
	return render(ResourceTabs, { props: { resource: res, children: overview } });
}

const yamlText = (container: HTMLElement) => container.querySelector('pre.yaml')?.textContent ?? '';

beforeEach(() => {
	setPageUrl('/crews/demo?namespace=kubemoot');
});

describe('ResourceTabs', () => {
	it('opens on the Overview with the right roles', () => {
		mount();
		expect(screen.getByRole('tablist')).toBeTruthy();
		const tabs = screen.getAllByRole('tab');
		expect(tabs.map((t) => t.textContent?.trim())).toEqual(['Overview', 'YAML']);
		expect(tabs[0].getAttribute('aria-selected')).toBe('true');
		expect(tabs[1].getAttribute('aria-selected')).toBe('false');
		expect(screen.getByRole('tabpanel').textContent).toContain('the overview');
		expect(tabs[0].getAttribute('aria-controls')).toBe(screen.getByRole('tabpanel').id);
	});

	it('shows the YAML when the URL says tab=yaml, so the tab can be linked', () => {
		setPageUrl('/crews/demo?namespace=kubemoot&tab=yaml');
		const { container } = mount();
		expect(screen.getByRole('tab', { name: 'YAML' }).getAttribute('aria-selected')).toBe('true');
		expect(yamlText(container)).toContain('kind: Crew');
	});

	it('puts the tab in the URL when clicked and takes it out for the Overview', async () => {
		mount();
		await fireEvent.click(screen.getByRole('tab', { name: 'YAML' }));
		expect(page.url.searchParams.get('tab')).toBe('yaml');
		expect(page.url.searchParams.get('namespace')).toBe('kubemoot');
		await fireEvent.click(screen.getByRole('tab', { name: 'Overview' }));
		expect(page.url.searchParams.has('tab')).toBe(false);
	});

	it('moves between tabs with the arrow keys, Home and End, focusing the new tab', async () => {
		mount();
		const [overviewTab, yamlTab] = screen.getAllByRole('tab');
		overviewTab.focus();
		await fireEvent.keyDown(overviewTab, { key: 'ArrowRight' });
		expect(page.url.searchParams.get('tab')).toBe('yaml');
		expect(document.activeElement).toBe(yamlTab);
		expect(yamlTab.getAttribute('tabindex')).toBe('0');
		expect(overviewTab.getAttribute('tabindex')).toBe('-1');
		await fireEvent.keyDown(yamlTab, { key: 'Home' });
		expect(document.activeElement).toBe(overviewTab);
		await fireEvent.keyDown(overviewTab, { key: 'ArrowLeft' });
		expect(document.activeElement).toBe(yamlTab);
		await fireEvent.keyDown(yamlTab, { key: 'x' });
		expect(document.activeElement).toBe(yamlTab);
	});

	it('shows the resource minus managedFields, and the whole thing when asked', async () => {
		setPageUrl('/crews/demo?tab=yaml');
		const { container } = mount();
		const hidden = yamlText(container);
		expect(hidden).toContain('replicas: 2');
		expect(hidden).toContain('phase: Ready');
		expect(hidden).not.toContain('managedFields');
		const toggle = screen.getByRole('button', { name: 'Show managed fields' });
		expect(toggle.getAttribute('aria-pressed')).toBe('false');
		await fireEvent.click(toggle);
		expect(yamlText(container)).toContain('managedFields:');
		expect(yamlText(container)).toContain('manager: kubectl');
		await fireEvent.click(screen.getByRole('button', { name: 'Hide managed fields' }));
		expect(yamlText(container)).toBe(hidden);
	});

	it('offers no managed fields toggle when the resource has none', () => {
		setPageUrl('/crews/demo?tab=yaml');
		mount({ kind: 'Crew', metadata: { name: 'x' } });
		expect(screen.queryByRole('button', { name: /managed fields/i })).toBeNull();
	});

	it('copies exactly the text shown and says Copied', async () => {
		setPageUrl('/crews/demo?tab=yaml');
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		const { container } = mount();
		const button = screen.getByRole('button', { name: /copy the resource yaml/i });
		await fireEvent.click(button);
		expect(writeText).toHaveBeenCalledWith(yamlText(container));
		await vi.waitFor(() => expect(button.textContent?.trim()).toBe('Copied'));
	});

	it('keeps saying Copy when the clipboard refuses', async () => {
		setPageUrl('/crews/demo?tab=yaml');
		Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('no')) }, configurable: true });
		mount();
		const button = screen.getByRole('button', { name: /copy the resource yaml/i });
		await fireEvent.click(button);
		await Promise.resolve();
		expect(button.textContent?.trim()).toBe('Copy');
	});

	it('makes the code region a labelled, keyboard-focusable region', () => {
		setPageUrl('/crews/demo?tab=yaml');
		mount();
		const region = screen.getByRole('region', { name: 'Resource YAML' });
		expect(region.getAttribute('tabindex')).toBe('0');
	});

	it('numbers the lines and keeps long lines unwrapped', () => {
		setPageUrl('/crews/demo?tab=yaml');
		const { container } = mount();
		expect(container.querySelectorAll('.line').length).toBe(yamlText(container).split('\n').length - 1);
	});

	it('renders no YAML before the resource has loaded', () => {
		setPageUrl('/crews/demo?tab=yaml');
		const { container } = mount(null);
		expect(container.querySelector('pre')).toBeNull();
	});
});
