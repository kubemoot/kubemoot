import { describe, expect, it } from 'vitest';
import {
	acceleratorMemoryTotalMiB,
	acceleratorMemoryUsedMiB,
	acceleratorModel,
	acceleratorOfEvent,
	loadedAcceleratorMemory,
	preferred
} from './accelerator-capacity';

describe('accelerator capacity readers', () => {
	it('read the current names', () => {
		const c = { acceleratorMemoryUsedMiB: 1, acceleratorMemoryTotalMiB: 2, acceleratorModel: 'new' };
		expect(acceleratorMemoryUsedMiB(c)).toBe(1);
		expect(acceleratorMemoryTotalMiB(c)).toBe(2);
		expect(acceleratorModel(c)).toBe('new');
		expect(loadedAcceleratorMemory({ acceleratorMemory: 7 })).toBe(7);
	});

	it('prefer the current names over the old ones', () => {
		const c = {
			acceleratorMemoryUsedMiB: 1,
			vramUsedMiB: 10,
			acceleratorMemoryTotalMiB: 2,
			vramTotalMiB: 20,
			acceleratorModel: 'new',
			gpuModel: 'old'
		};
		expect(acceleratorMemoryUsedMiB(c)).toBe(1);
		expect(acceleratorMemoryTotalMiB(c)).toBe(2);
		expect(acceleratorModel(c)).toBe('new');
		expect(loadedAcceleratorMemory({ acceleratorMemory: 7, sizeVram: 70 })).toBe(7);
	});

	it('fall back to the deprecated names of an older operator', () => {
		const c = { vramUsedMiB: 10, vramTotalMiB: 20, gpuModel: 'old' };
		expect(acceleratorMemoryUsedMiB(c)).toBe(10);
		expect(acceleratorMemoryTotalMiB(c)).toBe(20);
		expect(acceleratorModel(c)).toBe('old');
		expect(loadedAcceleratorMemory({ sizeVram: 70 })).toBe(70);
	});

	it('are undefined when the status gives nothing', () => {
		expect(acceleratorMemoryUsedMiB()).toBeUndefined();
		expect(acceleratorMemoryTotalMiB({})).toBeUndefined();
		expect(acceleratorModel({})).toBeUndefined();
		expect(acceleratorModel({ acceleratorModel: '' })).toBeUndefined();
		expect(loadedAcceleratorMemory()).toBeUndefined();
	});
});

describe('preferred', () => {
	it('keeps a current value, including 0', () => {
		expect(preferred(0, 5)).toBe(0);
		expect(preferred('new', 'old')).toBe('new');
	});

	it('takes the deprecated value when the current one is unset or empty', () => {
		expect(preferred(undefined, 'old')).toBe('old');
		expect(preferred('', 'old')).toBe('old');
	});
});

describe('acceleratorOfEvent', () => {
	it('prefers accelerator and falls back to gpu, never returning an empty name', () => {
		expect(acceleratorOfEvent({ accelerator: 'a', gpu: 'g' })).toBe('a');
		expect(acceleratorOfEvent({ gpu: 'g' })).toBe('g');
		expect(acceleratorOfEvent({ accelerator: '', gpu: 'g' })).toBe('g');
		expect(acceleratorOfEvent({ accelerator: '' })).toBeUndefined();
		expect(acceleratorOfEvent({})).toBeUndefined();
	});
});
