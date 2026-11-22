import { describe, expect, it } from 'vitest';
import { showsTick } from './hover-actions';

describe('showsTick', () => {
	it('is true for the copying icons only', () => {
		expect(showsTick('copy')).toBe(true);
		expect(showsTick('id')).toBe(true);
		expect(showsTick('edit')).toBe(false);
		expect(showsTick('delete')).toBe(false);
	});
});
