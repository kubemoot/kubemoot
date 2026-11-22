import { describe, expect, it } from 'vitest';
import { nodeAccelerator } from './node-accelerator';

describe('nodeAccelerator', () => {
	it('reads accelerator', () => {
		expect(nodeAccelerator({ accelerator: { present: true, count: 2 } })).toEqual({ present: true, count: 2 });
	});

	it('prefers accelerator over the deprecated gpu', () => {
		expect(nodeAccelerator({ accelerator: { present: true }, gpu: { present: false } })?.present).toBe(true);
	});

	it('falls back to gpu from an older server', () => {
		expect(nodeAccelerator({ gpu: { present: true, count: 4 } })).toEqual({ present: true, count: 4 });
	});

	it('is undefined when the node reports none', () => {
		expect(nodeAccelerator({})).toBeUndefined();
	});
});
