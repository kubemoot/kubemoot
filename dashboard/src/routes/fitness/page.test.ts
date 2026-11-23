import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setPageData } from '../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const suite = {
	apiVersion: 'kubemoot.ai/v1alpha1',
	kind: 'CrewFitnessSuite',
	metadata: { name: 'gate-run', namespace: 'crew-a', uid: 'u1', creationTimestamp: '2026-11-21T10:00:00Z' },
	spec: { crewRef: 'homelab-pilot', description: 'the gate run' },
	status: { phase: 'Completed', passed: 30, failed: 2, errored: 0, iterationsCompleted: 32, iterationsTotal: 32, artifactRef: { objectKey: 'crew-a/gate-run/report.xlsx', sizeBytes: 20480 } }
};

let deleteAnswer: () => Response;
let fetchMock: ReturnType<typeof vi.fn>;
const writeText = vi.fn();

beforeEach(() => {
	setPageData({ user: { username: 'jonathan' } });
	deleteAnswer = () => new Response(JSON.stringify({ deleted: true }), { status: 200 });
	fetchMock = vi.fn((url: string, init?: RequestInit) => {
		if (init?.method === 'DELETE') return Promise.resolve(deleteAnswer());
		const items = String(url).includes('crewfitnesssuites') ? [suite] : [];
		return Promise.resolve(new Response(JSON.stringify({ items }), { status: 200 }));
	});
	vi.stubGlobal('fetch', fetchMock);
	vi.stubGlobal('EventSource', class { close() {} onmessage = null; });
	writeText.mockReset().mockResolvedValue(undefined);
	Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
});

afterEach(() => vi.unstubAllGlobals());

async function openMenu() {
	render(Page);
	await screen.findByText('gate-run');
	await fireEvent.click(screen.getByRole('button', { name: 'Actions for gate-run' }));
	return screen.getByRole('menu', { name: 'Actions for gate-run' });
}

describe('a fitness run row menu', () => {
	it('offers Open, Copy summary, Download and Delete', async () => {
		const menu = await openMenu();
		expect(within(menu).getAllByRole('menuitem').map((e) => e.textContent?.trim())).toEqual(['Open', 'Copy summary', 'Download XLSX (20.0 KB)', 'Delete...']);
		const download = within(menu).getByRole('menuitem', { name: /Download XLSX/ });
		expect(download.getAttribute('href')).toBe('/api/kubemoot/crewfitnesssuites/crew-a/gate-run/artifact');
	});

	it('replaces the kmctl delete hint and the scattered report icons', async () => {
		render(Page);
		await screen.findByText('gate-run');
		expect(document.body.textContent).not.toContain('kmctl delete');
		expect(screen.queryByLabelText('Copy suite details')).toBeNull();
	});

	it('opens the run from the menu without the row click opening it twice', async () => {
		const menu = await openMenu();
		await fireEvent.click(within(menu).getByRole('menuitem', { name: 'Open' }));
		await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/iterations')));
		expect(document.querySelectorAll('.sub-cell').length).toBe(1);
	});

	it('copies the suite name and its summary as text', async () => {
		const menu = await openMenu();
		await fireEvent.click(within(menu).getByRole('menuitem', { name: 'Copy summary' }));
		await waitFor(() => expect(writeText).toHaveBeenCalledOnce());
		expect(writeText.mock.calls[0][0]).toContain('gate-run');
		expect(await screen.findByText('Copied')).toBeTruthy();
	});

	it('leaves the row closed when the menu button is pressed', async () => {
		await openMenu();
		expect(document.querySelectorAll('.sub-cell').length).toBe(0);
	});

	it('offers no Download for a run without a report', async () => {
		suite.status.artifactRef = undefined as unknown as typeof suite.status.artifactRef;
		const menu = await openMenu();
		expect(within(menu).queryByRole('menuitem', { name: /Download/ })).toBeNull();
		suite.status.artifactRef = { objectKey: 'crew-a/gate-run/report.xlsx', sizeBytes: 20480 };
	});

	it('offers no Delete to a visitor who is not signed in', async () => {
		setPageData({ user: null });
		const menu = await openMenu();
		expect(within(menu).queryByRole('menuitem', { name: /Delete/ })).toBeNull();
	});
});

describe('deleting a fitness run', () => {
	async function askToDelete() {
		const menu = await openMenu();
		await fireEvent.click(within(menu).getByRole('menuitem', { name: 'Delete...' }));
		return screen.getByRole('alertdialog');
	}

	it('asks first and names what goes, deleting nothing yet', async () => {
		const dialog = await askToDelete();
		expect(dialog.textContent).toContain('Delete the fitness run gate-run?');
		expect(dialog.textContent).toContain('Its 32 iteration results, transcripts and scores');
		expect(dialog.textContent).toContain('Its XLSX report (20.0 KB)');
		expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'DELETE')).toHaveLength(0);
	});

	it('deletes as the person and marks the row Deleting while the operator cleans up', async () => {
		await askToDelete();
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect(await screen.findByText('Deleting')).toBeTruthy();
		expect(fetchMock).toHaveBeenCalledWith('/api/kubemoot/crewfitnesssuites/crew-a/gate-run', { method: 'DELETE' });
		await fireEvent.click(screen.getByRole('button', { name: 'Actions for gate-run' }));
		expect(within(screen.getByRole('menu')).queryByRole('menuitem', { name: /Delete/ })).toBeNull();
	});

	it('answers 403 plainly and keeps the row', async () => {
		deleteAnswer = () => new Response(JSON.stringify({ error: 'You do not have permission to delete this fitness run. Your Kubernetes account is not allowed to; ask the cluster administrator for access.' }), { status: 403 });
		await askToDelete();
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toContain('You do not have permission to delete this fitness run');
		expect(screen.getAllByText('gate-run').length).toBeGreaterThan(0);
	});

	it('answers 404 when the run is already gone', async () => {
		deleteAnswer = () => new Response(JSON.stringify({ error: 'This fitness run no longer exists.' }), { status: 404 });
		await askToDelete();
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toBe('This fitness run no longer exists.');
	});

	it('keeps the run when the person says so', async () => {
		await askToDelete();
		await fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
		expect(screen.queryByRole('alertdialog')).toBeNull();
		expect(screen.getByText('gate-run')).toBeTruthy();
	});
});

describe('gates and judge score read apart', () => {
	const original = suite.status;
	afterEach(() => {
		suite.status = original;
	});

	it('labels the gate counts and the judge score as separate columns', async () => {
		suite.status = { ...original, judge: { phase: 'Complete', mean: 20 } } as unknown as typeof original;
		render(Page);
		await screen.findByText('gate-run');
		const headers = Array.from(document.querySelectorAll('.ftable thead th')).map((h) => h.textContent?.trim());
		expect(headers).toContain('Gates pass / fail / err i');
		expect(headers).toContain('Judge score');
		const cells = Array.from(document.querySelectorAll('.suite-row td')).map((c) => c.textContent?.trim());
		expect(cells).toContain('30/2/0');
		expect(cells.filter((c) => c === '20')).toHaveLength(1);
	});

	it('explains the two in an (i) note that opens on click', async () => {
		render(Page);
		await screen.findByText('gate-run');
		expect(screen.queryByRole('dialog')).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'About gates and judge score' }));
		expect(screen.getByRole('dialog').textContent).toContain('The two are independent');
	});

	it('shows status.error for an Error suite and the ToolSmoke message for ToolSmokeFailed', async () => {
		suite.status = { phase: 'Error', error: 'spec.scripts is empty' } as unknown as typeof original;
		const first = render(Page);
		expect(await screen.findByText('spec.scripts is empty')).toBeTruthy();
		first.unmount();
		suite.status = {
			phase: 'ToolSmokeFailed',
			conditions: [{ type: 'ToolSmoke', status: 'False', reason: 'Failed', message: '9 tool(s) failed the smoke, no model ran', lastTransitionTime: '' }]
		} as unknown as typeof original;
		render(Page);
		expect(await screen.findByText('9 tool(s) failed the smoke, no model ran')).toBeTruthy();
	});
});
