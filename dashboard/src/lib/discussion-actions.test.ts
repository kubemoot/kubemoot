import { describe, expect, it } from 'vitest';
import {
	canStartDiscussion,
	newDiscussionTooltip,
	NEW_DISCUSSION_NEEDS_CREW,
	NEW_DISCUSSION_TOOLTIP,
	pinTooltip,
	PIN_TOOLTIP,
	UNPIN_TOOLTIP
} from './discussion-actions';
import * as actions from './discussion-actions';

describe('New discussion rule', () => {
	it('is disabled with All crews selected (no scope)', () => {
		expect(canStartDiscussion(null)).toBe(false);
		expect(newDiscussionTooltip(null)).toBe(NEW_DISCUSSION_NEEDS_CREW);
	});

	it('is enabled with exactly one crew selected', () => {
		const scope = { namespace: 'pilot', crew: 'homelab-pilot' };
		expect(canStartDiscussion(scope)).toBe(true);
		expect(newDiscussionTooltip(scope)).toBe(NEW_DISCUSSION_TOOLTIP);
	});
});

describe('action tooltips', () => {
	it('state a purpose for pin and unpin', () => {
		expect(pinTooltip(false)).toBe(PIN_TOOLTIP);
		expect(pinTooltip(true)).toBe(UNPIN_TOOLTIP);
		expect(PIN_TOOLTIP).toMatch(/find again/);
	});

	it('every tooltip is non-empty and no two actions share a text', () => {
		const texts = Object.entries(actions)
			.filter(([k, v]) => k.endsWith('_TOOLTIP') && typeof v === 'string')
			.map(([, v]) => v as string);
		expect(texts.length).toBeGreaterThan(8);
		expect(texts.every((t) => t.length > 20)).toBe(true);
		expect(new Set(texts).size).toBe(texts.length);
	});
});
