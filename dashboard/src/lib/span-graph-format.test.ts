import { describe, expect, it } from 'vitest';
import { badgeWidth, fitLabel, formatSpanDuration, labelColumnWidth, spanTimingLabel } from './span-graph-format.js';

describe('formatSpanDuration', () => {
	it('prints ms and seconds', () => {
		expect(formatSpanDuration(450)).toBe('450ms');
		expect(formatSpanDuration(2900)).toBe('2.9s');
	});
	it.each([Infinity, -Infinity, NaN, undefined, null, -5])('prints a dash for %s', (v) => {
		expect(formatSpanDuration(v as number)).toBe('-');
	});
});

describe('spanTimingLabel', () => {
	it('spells inference out and never abbreviates it to inf', () => {
		const label = spanTimingLabel(52300, 7800, 6300);
		expect(label).toBe('52.3s (7.8s inference, 6.3s to first token)');
		expect(label).not.toMatch(/\binf\b/);
	});
	it('prints only the total without inference', () => {
		expect(spanTimingLabel(2900, 0)).toBe('2.9s');
		expect(spanTimingLabel(2900, undefined)).toBe('2.9s');
	});
	it('never prints Infinity, NaN, undefined or null', () => {
		const label = spanTimingLabel(Infinity, NaN, Infinity);
		expect(label).not.toMatch(/inf|nan|undefined|null/i);
	});
});

describe('label width', () => {
	it('grows for a long name and caps at 45% of the graph', () => {
		expect(labelColumnWidth(['a'], 0, 800)).toBe(160);
		expect(labelColumnWidth(['nvidia-gpu-advisor'], 60, 800)).toBeGreaterThan(160);
		expect(labelColumnWidth(['x'.repeat(200)], 60, 800)).toBe(360);
	});
	it('sizes a badge to its text', () => {
		expect(badgeWidth('vllm-rig1')).toBeGreaterThan(54);
	});
	it('shortens a name that cannot fit', () => {
		expect(fitLabel('short', 200)).toBe('short');
		expect(fitLabel('x'.repeat(60), 100)).toMatch(/\.\.\.$/);
	});
});
