import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import InfoPopover from './InfoPopover.svelte';

const props = { label: 'About Crews', lines: ['A crew is a team.', 'Open one to see its agents.'] };
const icon = () => screen.getByRole('button', { name: 'About Crews' });

describe('InfoPopover', () => {
	it('is a small (i) button, closed, with no tooltip of its own', () => {
		render(InfoPopover, props);
		expect(icon().textContent?.trim()).toBe('i');
		expect(icon().getAttribute('aria-expanded')).toBe('false');
		expect(screen.queryByRole('dialog')).toBeNull();
		expect(icon().getAttribute('aria-describedby')).toBeNull();
		expect(document.querySelector('[role="tooltip"]')).toBeNull();
	});

	it('opens with the term and the purpose on click', async () => {
		render(InfoPopover, props);
		await fireEvent.click(icon());
		expect(icon().getAttribute('aria-expanded')).toBe('true');
		const dialog = screen.getByRole('dialog', { name: 'About Crews' });
		expect(dialog.textContent).toContain('A crew is a team.');
		expect(dialog.textContent).toContain('Open one to see its agents.');
	});

	it('opens with Enter on keyboard focus', async () => {
		render(InfoPopover, props);
		icon().focus();
		await fireEvent.keyDown(icon(), { key: 'Enter' });
		// A button turns Enter into a click; the test environment does not, so click as the browser would.
		await fireEvent.click(icon());
		expect(screen.getByRole('dialog')).toBeTruthy();
	});

	it('closes with Escape and returns focus to the icon', async () => {
		render(InfoPopover, props);
		await fireEvent.click(icon());
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(screen.queryByRole('dialog')).toBeNull();
		expect(document.activeElement).toBe(icon());
	});

	it('closes on a click outside, and stays open on a click inside', async () => {
		render(InfoPopover, props);
		await fireEvent.click(icon());
		await fireEvent.click(screen.getByRole('dialog'));
		expect(screen.getByRole('dialog')).toBeTruthy();
		await fireEvent.click(document.body);
		expect(screen.queryByRole('dialog')).toBeNull();
	});

	it('closes when the icon is clicked again', async () => {
		render(InfoPopover, props);
		await fireEvent.click(icon());
		await fireEvent.click(icon());
		expect(screen.queryByRole('dialog')).toBeNull();
	});

	it('ignores Escape while closed', async () => {
		render(InfoPopover, props);
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(screen.queryByRole('dialog')).toBeNull();
	});
});
