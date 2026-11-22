import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from './auth/bearer-token';
import { callerToken, runWithIdentity } from './auth/identity';
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

describe('cachedRead for signed-in callers', () => {
	const as = (username: string, token: BearerToken) => <T>(fn: () => T) =>
		runWithIdentity({ kind: 'caller', username, token, ended: new AbortController().signal }, fn);

	it('never shares a read between two different callers', async () => {
		const read = vi.fn(async () => callerToken()?.reveal());
		const get = cachedRead(1000, read);
		const a = as('a', new BearerToken('ta'));
		const b = as('b', new BearerToken('tb'));
		expect(await a(get)).toBe('ta');
		expect(await b(get)).toBe('tb');
		expect(await a(get)).toBe('ta');
		expect(read).toHaveBeenCalledTimes(2);
	});
});
