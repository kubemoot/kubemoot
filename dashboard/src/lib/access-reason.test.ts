import { describe, expect, it } from 'vitest';
import { MAX_REASON_LENGTH, REASON_HEADER, validReason } from './access-reason.js';

describe('validReason', () => {
	it('is the trimmed text of 1 to 200 characters', () => {
		expect(validReason('  ticket 42 ')).toBe('ticket 42');
		expect(validReason('x'.repeat(MAX_REASON_LENGTH))).toHaveLength(MAX_REASON_LENGTH);
	});

	it('refuses nothing, blank, too long, and a control character such as a pasted newline', () => {
		expect(validReason(undefined)).toBeUndefined();
		expect(validReason(null)).toBeUndefined();
		expect(validReason('   ')).toBeUndefined();
		expect(validReason('x'.repeat(MAX_REASON_LENGTH + 1))).toBeUndefined();
		expect(validReason('one\ntwo')).toBeUndefined();
	});

	it('names the header the gateway reads', () => {
		expect(REASON_HEADER).toBe('Kubemoot-Access-Reason');
	});
});
