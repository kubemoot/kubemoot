import { describe, it, expect, afterEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { readBoolPref, writeBoolPref, showDebugDetail, showChatDetails } from './preferences';

function stubStorage(impl: Partial<Storage>) {
	vi.stubGlobal('localStorage', impl);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('readBoolPref', () => {
	it('returns the fallback when nothing is stored', () => {
		stubStorage({ getItem: () => null });
		expect(readBoolPref('k', false)).toBe(false);
		expect(readBoolPref('k', true)).toBe(true);
	});

	it('returns true when the stored value is "true"', () => {
		stubStorage({ getItem: () => 'true' });
		expect(readBoolPref('k', false)).toBe(true);
	});

	it('returns false for any other stored value', () => {
		stubStorage({ getItem: () => 'yes' });
		expect(readBoolPref('k', true)).toBe(false);
	});

	it('returns the fallback when getItem throws', () => {
		stubStorage({
			getItem: () => {
				throw new Error('blocked');
			}
		});
		expect(readBoolPref('k', true)).toBe(true);
	});
});

describe('writeBoolPref', () => {
	it('stores the value as a string', () => {
		const setItem = vi.fn();
		stubStorage({ setItem });
		writeBoolPref('k', true);
		expect(setItem).toHaveBeenCalledWith('k', 'true');
	});

	it('does not throw when setItem throws', () => {
		stubStorage({
			setItem: () => {
				throw new Error('quota');
			}
		});
		expect(() => writeBoolPref('k', true)).not.toThrow();
	});
});

describe('showDebugDetail', () => {
	it('defaults to false', () => {
		expect(get(showDebugDetail)).toBe(false);
	});

	it('toggles and persists even when storage throws', () => {
		stubStorage({
			setItem: () => {
				throw new Error('blocked');
			}
		});
		showDebugDetail.toggle();
		expect(get(showDebugDetail)).toBe(true);
		showDebugDetail.set(false);
		expect(get(showDebugDetail)).toBe(false);
	});
});

describe('showChatDetails', () => {
	it('defaults to off', () => {
		expect(get(showChatDetails)).toBe(false);
	});

	it('is remembered per viewer under its own key', () => {
		const setItem = vi.fn();
		stubStorage({ setItem });
		showChatDetails.set(true);
		expect(get(showChatDetails)).toBe(true);
		expect(setItem).toHaveBeenCalledWith('kubemoot-dashboard-show-chat-details', 'true');
		showChatDetails.set(false);
	});

	it('still toggles when storage throws', () => {
		stubStorage({
			setItem: () => {
				throw new Error('blocked');
			}
		});
		showChatDetails.toggle();
		expect(get(showChatDetails)).toBe(true);
		showChatDetails.toggle();
		expect(get(showChatDetails)).toBe(false);
	});
});
