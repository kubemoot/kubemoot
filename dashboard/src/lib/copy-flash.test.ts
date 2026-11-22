import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { COPIED_MS, CopyFlash } from './copy-flash.svelte';

const writeText = vi.fn();

beforeEach(() => {
	vi.useFakeTimers();
	writeText.mockReset().mockResolvedValue(undefined);
	Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
});
afterEach(() => vi.useRealTimers());

describe('CopyFlash', () => {
	it('copies the text and holds the key of the copy for a moment', async () => {
		const flash = new CopyFlash();
		expect(await flash.copy('row-1', 'hello')).toBe(true);
		expect(writeText).toHaveBeenCalledWith('hello');
		expect(flash.copied).toBe('row-1');
		vi.advanceTimersByTime(COPIED_MS);
		expect(flash.copied).toBeNull();
	});

	it('says nothing when the browser refuses', async () => {
		writeText.mockRejectedValue(new Error('denied'));
		const flash = new CopyFlash();
		expect(await flash.copy('row-1', 'hello')).toBe(false);
		expect(flash.copied).toBeNull();
	});

	it('follows the latest copy, and an older timer does not clear it', async () => {
		const flash = new CopyFlash();
		await flash.copy('a', '1');
		vi.advanceTimersByTime(COPIED_MS - 100);
		await flash.copy('b', '2');
		vi.advanceTimersByTime(200);
		expect(flash.copied).toBe('b');
		vi.advanceTimersByTime(COPIED_MS);
		expect(flash.copied).toBeNull();
	});
});
