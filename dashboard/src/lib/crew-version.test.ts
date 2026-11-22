import { describe, expect, it } from 'vitest';
import { crewVersionTooltip, formatCrewVersion } from './crew-version.js';

describe('formatCrewVersion', () => {
	it('drops the label form of a build suffix', () => {
		expect(formatCrewVersion('0.51.0-rc.2_5776d8b4e1a8')).toBe('0.51.0-rc.2');
	});
	it('drops a plus build suffix of any kind', () => {
		expect(formatCrewVersion('0.51.0-rc.2+5776d8b')).toBe('0.51.0-rc.2');
		expect(formatCrewVersion('1.2.3+build.7')).toBe('1.2.3');
	});
	it('leaves a plain final version alone', () => {
		expect(formatCrewVersion('1.2.3')).toBe('1.2.3');
	});
	it('does not cut an underscore that is not a hash', () => {
		expect(formatCrewVersion('1.2.3_beta')).toBe('1.2.3_beta');
	});
	it('handles empty input and a leading v', () => {
		expect(formatCrewVersion(undefined)).toBe('');
		expect(formatCrewVersion('')).toBe('');
		expect(formatCrewVersion('v1.2.3_abcdef12')).toBe('1.2.3');
	});
});

describe('crewVersionTooltip', () => {
	it('keeps the full value when it differs', () => {
		expect(crewVersionTooltip('0.51.0-rc.2_5776d8b4e1a8')).toBe('Crew version 0.51.0-rc.2 (label value 0.51.0-rc.2_5776d8b4e1a8)');
	});
	it('names a plain version once', () => {
		expect(crewVersionTooltip('1.2.3')).toBe('Crew version 1.2.3');
		expect(crewVersionTooltip('')).toBe('');
	});
});
