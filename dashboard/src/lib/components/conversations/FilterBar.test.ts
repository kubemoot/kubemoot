import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import FilterBar from './FilterBar.svelte';
import { NO_FILTER } from '#lib/conversation-filter.js';

const base = () => ({ filter: NO_FILTER, signedIn: true, loaded: 3, hasOlder: false, onchange: vi.fn(), onloadolder: vi.fn() });

describe('FilterBar', () => {
	it('keeps search, status, Mine and one date control in two rows, with no bare date boxes', () => {
		const { container } = render(FilterBar, base());
		expect(container.querySelectorAll('input[type="date"]')).toHaveLength(0);
		expect(screen.getByRole('button', { name: 'Any date' })).toBeTruthy();
		expect(screen.getByLabelText('Status')).toBeTruthy();
		expect(screen.getByLabelText(/Mine/)).toBeTruthy();
		expect(container.querySelectorAll('.search-row, .row')).toHaveLength(2);
	});

	it('opens the date range with From and To labelled, and sets a day', async () => {
		const props = base();
		render(FilterBar, props);
		await fireEvent.click(screen.getByRole('button', { name: 'Any date' }));
		const from = screen.getByLabelText('From');
		expect(screen.getByLabelText('To')).toBeTruthy();
		await fireEvent.change(from, { target: { value: '2026-11-01' } });
		expect(props.onchange).toHaveBeenCalledWith(expect.objectContaining({ from: '2026-11-01' }));
	});

	it('says the range that is set on the button', () => {
		render(FilterBar, { ...base(), filter: { ...NO_FILTER, from: '2026-11-01', to: '2026-11-21' } });
		expect(screen.getByRole('button', { name: '2026-11-01 to 2026-11-21' })).toBeTruthy();
	});

	it('closes the date range on a click elsewhere', async () => {
		render(FilterBar, base());
		await fireEvent.click(screen.getByRole('button', { name: 'Any date' }));
		await fireEvent.click(document.body);
		expect(screen.queryByLabelText('From')).toBeNull();
	});
});
