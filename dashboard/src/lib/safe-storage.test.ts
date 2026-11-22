import { afterEach, describe, expect, it, vi } from 'vitest';
import { readItem, writeItem } from './safe-storage';

afterEach(() => {
	vi.unstubAllGlobals();
	localStorage.clear();
});

describe('safe storage', () => {
	it('keeps and reads text, and reads null for a key never written', () => {
		expect(readItem('k')).toBeNull();
		writeItem('k', 'v');
		expect(readItem('k')).toBe('v');
	});

	it('does nothing when storage refuses, throws on access, or is missing', () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('full');
			}
		});
		expect(readItem('k')).toBeNull();
		expect(() => writeItem('k', 'v')).not.toThrow();
		vi.stubGlobal('localStorage', undefined);
		expect(readItem('k')).toBeNull();
		expect(() => writeItem('k', 'v')).not.toThrow();
	});
});
