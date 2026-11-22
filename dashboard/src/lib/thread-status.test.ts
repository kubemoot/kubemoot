import { describe, expect, it } from 'vitest';
import { statusLabel } from './thread-status.js';

describe('statusLabel', () => {
	it('names each status in words', () => {
		expect(statusLabel('open')).toBe('Active');
		expect(statusLabel('synthesized')).toBe('Answered');
		expect(statusLabel('closed')).toBe('Closed');
	});

	it('calls anything else closed', () => {
		expect(statusLabel('')).toBe('Closed');
		expect(statusLabel('whatever')).toBe('Closed');
	});
});
