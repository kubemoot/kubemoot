import { afterEach, describe, expect, it, vi } from 'vitest';
import { HOVER_GRACE_MS, TOOLTIP_DELAY_MS, tooltip } from './tooltip.js';

afterEach(() => {
	vi.useRealTimers();
	document.body.replaceChildren();
});

function setup(param: Parameters<typeof tooltip>[1] = 'Copy: the text') {
	const button = document.createElement('button');
	button.textContent = 'Copy';
	document.body.append(button);
	const action = tooltip(button, param);
	const tip = () => document.querySelector('[role="tooltip"]') as HTMLElement;
	return { button, action, tip };
}

describe('tooltip', () => {
	it('adds a described-by link and keeps the control name', () => {
		const { button, tip } = setup();
		expect(button.getAttribute('aria-describedby')).toBe(tip().id);
		expect(button.textContent).toBe('Copy');
		expect(tip().textContent).toBe('Copy: the text');
	});

	it('keeps an existing description', () => {
		const button = document.createElement('button');
		button.setAttribute('aria-describedby', 'other');
		document.body.append(button);
		tooltip(button, 'x');
		expect(button.getAttribute('aria-describedby')).toMatch(/^other km-tip-\d+$/);
	});

	it('opens on focus at once', () => {
		const { button, tip } = setup();
		button.dispatchEvent(new FocusEvent('focus'));
		expect(tip().hasAttribute('data-open')).toBe(true);
		button.dispatchEvent(new FocusEvent('blur'));
		expect(tip().hasAttribute('data-open')).toBe(false);
	});

	it('opens on hover only after the pause', () => {
		vi.useFakeTimers();
		const { button, tip } = setup();
		button.dispatchEvent(new MouseEvent('mouseenter'));
		vi.advanceTimersByTime(TOOLTIP_DELAY_MS - 1);
		expect(tip().hasAttribute('data-open')).toBe(false);
		vi.advanceTimersByTime(1);
		expect(tip().hasAttribute('data-open')).toBe(true);
	});

	it('does not open when the pointer leaves first', () => {
		vi.useFakeTimers();
		const { button, tip } = setup();
		button.dispatchEvent(new MouseEvent('mouseenter'));
		button.dispatchEvent(new MouseEvent('mouseleave'));
		vi.advanceTimersByTime(TOOLTIP_DELAY_MS * 2);
		expect(tip().hasAttribute('data-open')).toBe(false);
	});

	it('closes on Escape and on click', () => {
		const { button, tip } = setup();
		button.dispatchEvent(new FocusEvent('focus'));
		window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
		expect(tip().hasAttribute('data-open')).toBe(false);
		button.dispatchEvent(new FocusEvent('focus'));
		button.click();
		expect(tip().hasAttribute('data-open')).toBe(false);
	});

	it('stays open while the pointer is on the tooltip, then closes', () => {
		vi.useFakeTimers();
		const { button, tip } = setup();
		button.dispatchEvent(new FocusEvent('focus'));
		button.dispatchEvent(new MouseEvent('mouseleave'));
		tip().dispatchEvent(new MouseEvent('mouseenter'));
		vi.advanceTimersByTime(HOVER_GRACE_MS * 2);
		expect(tip().hasAttribute('data-open')).toBe(true);
		tip().dispatchEvent(new MouseEvent('mouseleave'));
		vi.advanceTimersByTime(HOVER_GRACE_MS + 1);
		expect(tip().hasAttribute('data-open')).toBe(false);
	});

	it('does not flash open when a mouse click focuses the control', () => {
		const { button, tip } = setup();
		button.dispatchEvent(new MouseEvent('mousedown'));
		button.dispatchEvent(new FocusEvent('focus'));
		expect(tip().hasAttribute('data-open')).toBe(false);
		button.dispatchEvent(new MouseEvent('mouseup'));
		button.dispatchEvent(new FocusEvent('focus'));
		expect(tip().hasAttribute('data-open')).toBe(true);
	});

	it('closes on scroll', () => {
		const { button, tip } = setup();
		button.dispatchEvent(new FocusEvent('focus'));
		window.dispatchEvent(new Event('scroll'));
		expect(tip().hasAttribute('data-open')).toBe(false);
	});

	it('never opens an empty tooltip', () => {
		const { button, tip } = setup('');
		button.dispatchEvent(new FocusEvent('focus'));
		expect(tip().hasAttribute('data-open')).toBe(false);
	});

	it('keeps only one tooltip open: a hover closes one opened by focus', () => {
		vi.useFakeTimers();
		const first = setup('First');
		const second = setup('Second');
		first.button.dispatchEvent(new FocusEvent('focus'));
		expect(first.tip().hasAttribute('data-open')).toBe(true);
		second.button.dispatchEvent(new MouseEvent('mouseenter'));
		vi.advanceTimersByTime(TOOLTIP_DELAY_MS);
		const open = [...document.querySelectorAll('[role="tooltip"]')].filter((t) => t.hasAttribute('data-open'));
		expect(open.map((t) => t.textContent)).toEqual(['Second']);
	});

	it('keeps only one tooltip open: a focus closes one opened by hover', () => {
		vi.useFakeTimers();
		const first = setup('First');
		const second = setup('Second');
		first.button.dispatchEvent(new MouseEvent('mouseenter'));
		vi.advanceTimersByTime(TOOLTIP_DELAY_MS);
		second.button.dispatchEvent(new FocusEvent('focus'));
		const open = [...document.querySelectorAll('[role="tooltip"]')].filter((t) => t.hasAttribute('data-open'));
		expect(open.map((t) => t.textContent)).toEqual(['Second']);
	});

	it('puts a second line in a quieter style', () => {
		const { tip } = setup('Term.\nPurpose.');
		expect(tip().querySelectorAll('.km-tip-quiet').length).toBe(1);
	});

	it('updates its text and cleans up on destroy', () => {
		const { button, action, tip } = setup();
		action.update('New text');
		expect(tip().textContent).toBe('New text');
		action.destroy();
		expect(document.querySelector('[role="tooltip"]')).toBeNull();
		expect(button.hasAttribute('aria-describedby')).toBe(false);
	});

	it('places a right-hand tooltip to the right of the control', () => {
		const { button, tip } = setup({ text: 'Menu', placement: 'right' });
		button.getBoundingClientRect = () => ({ left: 0, right: 200, top: 100, bottom: 130, width: 200, height: 30, x: 0, y: 100, toJSON: () => '' });
		button.dispatchEvent(new FocusEvent('focus'));
		expect(Number.parseInt(tip().style.left, 10)).toBeGreaterThanOrEqual(208);
	});
});
