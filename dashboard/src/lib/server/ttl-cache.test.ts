import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cachedRead } from './ttl-cache';

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('cachedRead', () => {
	it('shares one read among callers inside the interval', async () => {
		const read = vi.fn(async () => 1);
		const get = cachedRead(1000, read);
		await Promise.all([get(), get(), get()]);
		expect(read).toHaveBeenCalledTimes(1);
	});

	it('reads again after the interval', async () => {
		const read = vi.fn(async () => 1);
		const get = cachedRead(1000, read);
		await get();
		vi.advanceTimersByTime(1001);
		await get();
		expect(read).toHaveBeenCalledTimes(2);
	});

	it('does not keep a failed read', async () => {
		const read = vi.fn().mockRejectedValueOnce(new Error('down')).mockResolvedValue(2);
		const get = cachedRead(1000, read);
		await expect(get()).rejects.toThrow('down');
		await Promise.resolve();
		expect(await get()).toBe(2);
	});
});
