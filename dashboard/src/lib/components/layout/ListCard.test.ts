import { fireEvent, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import ListCard from './ListCard.svelte';

const question = createRawSnippet(() => ({ render: () => '<span>the question</span>' }));

describe('ListCard', () => {
	it('has no icon bar without actions', () => {
		const { container } = render(ListCard, { props: { selected: false, onopen: () => undefined, children: question } });
		expect(container.querySelector('[role="toolbar"]')).toBeNull();
	});

	it('marks the row for the hover style and carries its actions as named icon buttons', async () => {
		const run = vi.fn();
		const { container } = render(ListCard, {
			props: { selected: false, onopen: () => undefined, children: question, actions: [{ label: 'Copy id', id: 'copy-id', icon: 'id', run }] }
		});
		expect(container.querySelector('.row')?.classList.contains('has-hover-actions')).toBe(true);
		await fireEvent.click(screen.getByRole('button', { name: 'Copy id' }));
		expect(run).toHaveBeenCalledOnce();
	});

	it('does not open the row when an action is pressed', async () => {
		const onopen = vi.fn();
		render(ListCard, { props: { selected: false, onopen, children: question, actions: [{ label: 'Copy id', id: 'copy-id', icon: 'id', run: () => undefined }] } });
		await fireEvent.click(screen.getByRole('button', { name: 'Copy id' }));
		expect(onopen).not.toHaveBeenCalled();
	});

	it('keeps the icon buttons after the card in the tab order, so the keyboard reaches them', () => {
		render(ListCard, { props: { selected: false, onopen: () => undefined, children: question, actions: [{ label: 'Copy id', id: 'copy-id', icon: 'id', run: () => undefined }] } });
		const buttons = screen.getAllByRole('button');
		expect(buttons.at(-1)?.getAttribute('aria-label')).toBe('Copy id');
	});
});
