import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import FilterBar from './FilterBar.svelte';
import { NO_FILTER } from '#lib/conversation-filter.js';

const base = () => ({ filter: NO_FILTER, signedIn: true, loaded: 3, hasOlder: false, onchange: vi.fn(), onloadolder: vi.fn() });

const range = () => screen.getByLabelText<HTMLSelectElement>('Time range');

describe('FilterBar', () => {
	it('keeps search, status, Mine and one time dropdown in two rows, with no bare date boxes', () => {
		const { container } = render(FilterBar, base());
		expect(container.querySelectorAll('input[type="date"], input[type="datetime-local"]')).toHaveLength(0);
		expect(range()).toBeTruthy();
		expect(screen.getByLabelText('Status')).toBeTruthy();
		expect(screen.getByLabelText(/Mine/)).toBeTruthy();
		expect(container.querySelectorAll('.search-row, .row')).toHaveLength(2);
	});

	it('lists the ranges in order, Anytime first and Custom last', () => {
		render(FilterBar, base());
		expect([...range().options].map((o) => o.textContent)).toEqual([
			'Anytime', 'Last 10 minutes', 'Last hour', 'Last 6 hours', 'Today', 'Last 24 hours', 'Last 3 days', 'This week', 'This month', 'Last 3 months', 'Custom...'
		]);
	});

	it('shows the range that is set and sets another, clearing any custom days', async () => {
		const props = { ...base(), filter: { ...NO_FILTER, range: '24h' as const } };
		render(FilterBar, props);
		expect(range().value).toBe('24h');
		await fireEvent.change(range(), { target: { value: '1h' } });
		expect(props.onchange).toHaveBeenCalledWith(expect.objectContaining({ range: '1h', from: null, to: null }));
	});

	it('Custom opens From and To date-and-time inputs and sets them', async () => {
		const props = base();
		render(FilterBar, props);
		await fireEvent.change(range(), { target: { value: 'custom' } });
		expect(screen.getByRole('group', { name: 'Custom time range' })).toBeTruthy();
		expect(props.onchange).toHaveBeenCalledWith(expect.objectContaining({ range: 'custom' }));
		const from = screen.getByLabelText<HTMLInputElement>('From');
		expect(from.type).toBe('datetime-local');
		expect(screen.getByLabelText<HTMLInputElement>('To').type).toBe('datetime-local');
		await fireEvent.change(from, { target: { value: '2026-11-01T09:30' } });
		expect(props.onchange).toHaveBeenLastCalledWith(expect.objectContaining({ range: 'custom', from: '2026-11-01T09:30' }));
	});

	it('says the custom range that is set on a button that reopens the inputs', async () => {
		render(FilterBar, { ...base(), filter: { ...NO_FILTER, range: 'custom', from: '2026-11-01T09:00', to: '2026-11-21T17:30' } });
		const button = screen.getByRole('button', { name: 'Nov 1, 09:00 to Nov 21, 17:30' });
		expect(screen.queryByLabelText('From')).toBeNull();
		await fireEvent.click(button);
		expect((screen.getByLabelText<HTMLInputElement>('From')).value).toBe('2026-11-01T09:00');
	});

	it('closes the custom inputs on Escape and on a click elsewhere', async () => {
		render(FilterBar, { ...base(), filter: { ...NO_FILTER, range: 'custom', from: '2026-11-01T09:00' } });
		await fireEvent.click(screen.getByRole('button', { name: /^From Nov 1/ }));
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(screen.queryByLabelText('From')).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: /^From Nov 1/ }));
		await fireEvent.click(document.body);
		expect(screen.queryByLabelText('From')).toBeNull();
	});

	it('shows a chip for the time range that removes it to Anytime', async () => {
		const props = { ...base(), filter: { ...NO_FILTER, range: '6h' as const } };
		render(FilterBar, props);
		await fireEvent.click(screen.getByRole('button', { name: 'Remove filter Last 6 hours' }));
		expect(props.onchange).toHaveBeenCalledWith(expect.objectContaining({ range: 'any' }));
	});

	it('gives Mine a short tooltip and nothing else in the bar a tooltip', () => {
		render(FilterBar, base());
		const mine = screen.getByText('Mine').closest('label') as HTMLElement;
		expect(document.getElementById(mine.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Only conversations you started');
		expect(screen.getAllByRole('tooltip', { hidden: true })).toHaveLength(1);
	});
});
