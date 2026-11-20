import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import CommandHint from './CommandHint.svelte';

afterEach(() => {
	vi.restoreAllMocks();
});

describe('CommandHint', () => {
	it('shows the label and the command as text', () => {
		render(CommandHint, { label: 'Remove a crew:', command: 'kmctl delete crew demo' });
		expect(screen.getByText('Remove a crew:')).toBeTruthy();
		expect(screen.getByText('kmctl delete crew demo')).toBeTruthy();
	});

	it('copies the command and says so', async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(CommandHint, { label: 'x', command: 'kmctl status' });
		await fireEvent.click(screen.getByRole('button', { name: /copy the command/i }));
		expect(writeText).toHaveBeenCalledWith('kmctl status');
		await waitFor(() => expect(screen.getByRole('button').textContent?.trim()).toBe('Copied'));
	});

	it('keeps saying Copy when the clipboard refuses', async () => {
		const writeText = vi.fn().mockRejectedValue(new Error('denied'));
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(CommandHint, { label: 'x', command: 'kmctl status' });
		await fireEvent.click(screen.getByRole('button'));
		await Promise.resolve();
		expect(screen.getByRole('button').textContent?.trim()).toBe('Copy');
	});
});
