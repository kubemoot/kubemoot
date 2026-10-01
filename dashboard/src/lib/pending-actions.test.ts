import { describe, expect, it } from 'vitest';
import { isSettled, unsettledActions } from './pending-actions';

const NOW = 1_000_000;
const CUTOFF = NOW - 60_000;

describe('isSettled', () => {
	const loaded = new Set(['qwen3:8b']);

	it('settles a load once the model is loaded, and an unload once it is gone', () => {
		expect(isSettled('qwen3:8b', { action: 'load', startedAt: NOW }, loaded, CUTOFF)).toBe(true);
		expect(isSettled('llama3:8b', { action: 'unload', startedAt: NOW }, loaded, CUTOFF)).toBe(true);
	});

	it('keeps an action open while the status has not caught up', () => {
		expect(isSettled('llama3:8b', { action: 'load', startedAt: NOW }, loaded, CUTOFF)).toBe(false);
		expect(isSettled('qwen3:8b', { action: 'unload', startedAt: NOW }, loaded, CUTOFF)).toBe(false);
	});

	it('gives up on an action older than the cutoff', () => {
		const old = { action: 'load' as const, startedAt: CUTOFF - 1 };
		expect(isSettled('llama3:8b', old, loaded, CUTOFF)).toBe(true);
	});
});

describe('unsettledActions', () => {
	it('returns null when nothing settled', () => {
		const pending = { 'llama3:8b': { action: 'load' as const, startedAt: NOW } };
		expect(unsettledActions(pending, new Set(), CUTOFF)).toBeNull();
		expect(unsettledActions({}, new Set(), CUTOFF)).toBeNull();
	});

	it('drops the settled actions and keeps the rest', () => {
		const pending = {
			'qwen3:8b': { action: 'load' as const, startedAt: NOW },
			'llama3:8b': { action: 'load' as const, startedAt: NOW }
		};
		expect(unsettledActions(pending, new Set(['qwen3:8b']), CUTOFF)).toEqual({
			'llama3:8b': { action: 'load', startedAt: NOW }
		});
	});
});
