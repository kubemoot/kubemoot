import { describe, expect, it } from 'vitest';
import * as actions from './discussion-actions';

describe('action tooltips', () => {
	it('every tooltip is non-empty and no two actions share a text', () => {
		const texts = Object.entries(actions)
			.filter(([k, v]) => k.endsWith('_TOOLTIP') && typeof v === 'string')
			.map(([, v]) => v as string);
		expect(texts.length).toBe(3);
		expect(texts.every((t) => t.length > 20)).toBe(true);
		expect(new Set(texts).size).toBe(texts.length);
	});

	it('offers no tooltip for an action that changes a discussion', () => {
		const names = Object.keys(actions);
		for (const gone of ['DELETE_TOOLTIP', 'PIN_TOOLTIP', 'PAUSE_TOOLTIP', 'STOP_TOOLTIP', 'REPLAY_TOOLTIP']) {
			expect(names).not.toContain(gone);
		}
	});
});
