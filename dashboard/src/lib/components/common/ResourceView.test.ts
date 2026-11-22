import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ResourceView from './ResourceView.svelte';
import { page, setPageUrl } from '../../../../vitest-mocks/app-state.svelte.js';

const resource = {
	apiVersion: 'kubemoot.ai/v1alpha1',
	kind: 'Crew',
	metadata: { name: 'demo', namespace: 'kubemoot', managedFields: [{ manager: 'kubectl' }] },
	spec: { replicas: 2, enabled: true },
	status: { phase: 'Ready' }
};

const description = createRawSnippet(() => ({ render: () => '<p>the description</p>' }));

function mount(res: object | null = resource) {
	return render(ResourceView, { props: { resource: res, children: description } });
}

const codeText = (container: HTMLElement) => container.querySelector('pre.code')?.textContent ?? '';
const selector = () => screen.getByRole<HTMLSelectElement>('combobox', { name: 'View' });

beforeEach(() => {
	setPageUrl('/crews/demo?namespace=kubemoot');
});

describe('ResourceView selector', () => {
	it('opens on the Description with one small selector offering Description, YAML and JSON', () => {
		const { container } = mount();
		expect([...selector().options].map((o) => o.textContent)).toEqual(['Description', 'YAML', 'JSON']);
		expect(selector().value).toBe('description');
		expect(screen.getByText('the description').closest('[hidden]')).toBeNull();
		expect(container.querySelector('pre')).toBeNull();
	});

	it('has no tab list and no row of buttons', () => {
		mount();
		expect(screen.queryByRole('tablist')).toBeNull();
		expect(screen.queryByRole('tab')).toBeNull();
		expect(screen.queryByRole('button', { name: /copy/i })).toBeNull();
	});

	it('shows the YAML when the URL says view=yaml, so the view can be linked', () => {
		setPageUrl('/crews/demo?namespace=kubemoot&view=yaml');
		const { container } = mount();
		expect(selector().value).toBe('yaml');
		expect(codeText(container)).toContain('kind: Crew');
		expect(screen.getByText('the description').closest('[hidden]')).not.toBeNull();
	});

	it('shows the JSON when the URL says view=json', () => {
		setPageUrl('/crews/demo?view=json');
		const { container } = mount();
		expect(selector().value).toBe('json');
		expect(JSON.parse(codeText(container)).kind).toBe('Crew');
		expect(screen.getByRole('region', { name: 'Resource JSON' })).toBeTruthy();
	});

	it('still opens the YAML for a link made with the older tab parameter', () => {
		setPageUrl('/crews/demo?tab=yaml');
		const { container } = mount();
		expect(codeText(container)).toContain('kind: Crew');
	});

	it('puts the view in the URL when chosen and takes it out for the Description', async () => {
		mount();
		await fireEvent.change(selector(), { target: { value: 'json' } });
		expect(page.url.searchParams.get('view')).toBe('json');
		expect(page.url.searchParams.get('namespace')).toBe('kubemoot');
		await fireEvent.change(selector(), { target: { value: 'description' } });
		expect(page.url.searchParams.has('view')).toBe(false);
	});

	it('colours JSON keys and values with the shared kinds', () => {
		setPageUrl('/crews/demo?view=json');
		const { container } = mount();
		expect(container.querySelector('pre.code .key')?.textContent).toBe('"apiVersion"');
		expect(container.querySelector('pre.code .number')?.textContent).toBe('2');
		expect(container.querySelector('pre.code .bool')?.textContent).toBe('true');
	});

	it('numbers the lines and keeps long lines unwrapped', () => {
		setPageUrl('/crews/demo?view=yaml');
		const { container } = mount();
		expect(container.querySelectorAll('.line').length).toBe(codeText(container).split('\n').length - 1);
	});

	it('makes the code region a labelled, keyboard-focusable region', () => {
		setPageUrl('/crews/demo?view=yaml');
		mount();
		expect(screen.getByRole('region', { name: 'Resource YAML' }).getAttribute('tabindex')).toBe('0');
	});

	it('renders no code before the resource has loaded', () => {
		setPageUrl('/crews/demo?view=yaml');
		const { container } = mount(null);
		expect(container.querySelector('pre')).toBeNull();
		expect(screen.queryByRole('button', { name: 'View options' })).toBeNull();
	});
});

describe('the view options menu', () => {
	const openMenu = async () => {
		await fireEvent.click(screen.getByRole('button', { name: 'View options' }));
		return screen.getByRole('menu', { name: 'View options' });
	};

	it('is offered beside the selector for YAML and JSON, and not for the Description', () => {
		mount();
		expect(screen.queryByRole('button', { name: 'View options' })).toBeNull();
	});

	it('holds Copy and Show managed fields, and nothing else', async () => {
		setPageUrl('/crews/demo?view=yaml');
		mount();
		const menu = await openMenu();
		expect(within(menu).getByRole('menuitem', { name: 'Copy YAML' })).toBeTruthy();
		expect(within(menu).getByRole('menuitemcheckbox', { name: 'Show managed fields' }).getAttribute('aria-checked')).toBe('false');
		expect(within(menu).getAllByRole('menuitem')).toHaveLength(1);
	});

	it('shows the resource minus managedFields, and the whole thing when asked, in YAML and in JSON', async () => {
		setPageUrl('/crews/demo?view=yaml');
		const { container } = mount();
		const hidden = codeText(container);
		expect(hidden).toContain('replicas: 2');
		expect(hidden).not.toContain('managedFields');
		await fireEvent.click(within(await openMenu()).getByRole('menuitemcheckbox', { name: 'Show managed fields' }));
		expect(codeText(container)).toContain('managedFields:');
		expect(codeText(container)).toContain('manager: kubectl');
		await fireEvent.change(selector(), { target: { value: 'json' } });
		expect(codeText(container)).toContain('"managedFields"');
		await fireEvent.click(within(await openMenu()).getByRole('menuitemcheckbox', { name: 'Show managed fields' }));
		expect(codeText(container)).not.toContain('managedFields');
	});

	it('offers no managed fields item when the resource has none', async () => {
		setPageUrl('/crews/demo?view=yaml');
		mount({ kind: 'Crew', metadata: { name: 'x' } });
		expect(within(await openMenu()).queryByRole('menuitemcheckbox')).toBeNull();
	});

	it('copies exactly the text shown, in the shown format, and says Copied', async () => {
		setPageUrl('/crews/demo?view=json');
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		const { container } = mount();
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Copy JSON' }));
		expect(writeText).toHaveBeenCalledWith(codeText(container));
		expect((await screen.findByRole('status')).textContent).toBe('Copied');
	});

	it('says nothing when the clipboard refuses', async () => {
		setPageUrl('/crews/demo?view=yaml');
		Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('no')) }, configurable: true });
		mount();
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Copy YAML' }));
		await Promise.resolve();
		expect(screen.queryByRole('status')).toBeNull();
	});
});
