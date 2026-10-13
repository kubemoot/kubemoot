import { describe, expect, it } from 'vitest';
import { isReleaseCandidate } from './release-candidate';

describe('isReleaseCandidate', () => {
	it('is true for a release candidate', () => {
		expect(isReleaseCandidate('0.355.0-rc.1')).toBe(true);
		expect(isReleaseCandidate('v1.2.3-rc.24')).toBe(true);
	});

	it('is false for a final version', () => {
		expect(isReleaseCandidate('0.354.1')).toBe(false);
		expect(isReleaseCandidate('v1.0.0')).toBe(false);
	});

	it('is false for a malformed version', () => {
		for (const version of ['', '...', 'dev', '1.2', '0.1.0-rc', '0.1.0-rc.x', '0.1.0-beta.1', ' 0.1.0-rc.1']) {
			expect(isReleaseCandidate(version)).toBe(false);
		}
		expect(isReleaseCandidate(undefined)).toBe(false);
		expect(isReleaseCandidate(null)).toBe(false);
	});
});
