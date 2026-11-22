// The one tooltip behind every page title, menu item and action in the dashboard.
// Use it as a Svelte action: <button use:tooltip={ACTIONS.copyDiscussion}>.
//
// The tooltip is a real element (role="tooltip") that the control points at with
// aria-describedby, so a screen reader reads it as the control's description and the
// control keeps its own name. It opens on hover after a short pause and at once on
// keyboard focus, and closes on leave, blur, click and Escape.

export interface TooltipOptions {
	/** The text. A line break starts a second, quieter line (term, then purpose). */
	text: string;
	/** Where it opens: under the control (default) or to its right (the left menu). */
	placement?: 'below' | 'right';
	/** Pause before a hover opens it, in milliseconds. Focus opens it at once. */
	delay?: number;
}

export type TooltipParam = string | TooltipOptions;

export const TOOLTIP_DELAY_MS = 400;
const GAP_PX = 8;
const EDGE_PX = 8;

let counter = 0;

function normalize(param: TooltipParam): Required<TooltipOptions> {
	const opts = typeof param === 'string' ? { text: param } : param;
	return { placement: 'below', delay: TOOLTIP_DELAY_MS, ...opts };
}

function fillText(tip: HTMLElement, text: string) {
	tip.replaceChildren();
	text.split('\n').forEach((line, i) => {
		const part = document.createElement('span');
		part.className = i === 0 ? 'km-tip-line' : 'km-tip-line km-tip-quiet';
		part.textContent = line;
		tip.append(part);
	});
}

function place(tip: HTMLElement, node: HTMLElement, placement: 'below' | 'right') {
	const rect = node.getBoundingClientRect();
	const width = tip.offsetWidth;
	const height = tip.offsetHeight;
	const maxLeft = Math.max(EDGE_PX, window.innerWidth - width - EDGE_PX);
	if (placement === 'right') {
		tip.style.left = `${Math.min(rect.right + GAP_PX, maxLeft)}px`;
		const top = rect.top + rect.height / 2 - height / 2;
		tip.style.top = `${Math.max(EDGE_PX, top)}px`;
		return;
	}
	tip.style.left = `${Math.max(EDGE_PX, Math.min(rect.left, maxLeft))}px`;
	tip.style.top = `${rect.bottom + GAP_PX}px`;
}

export const HOVER_GRACE_MS = 150;

export function tooltip(node: HTMLElement, param: TooltipParam) {
	let opts = normalize(param);
	const tip = document.createElement('div');
	tip.id = `km-tip-${++counter}`;
	tip.setAttribute('role', 'tooltip');
	tip.className = 'km-tip';
	fillText(tip, opts.text);
	document.body.append(tip);

	const previous = node.getAttribute('aria-describedby');
	node.setAttribute('aria-describedby', previous ? `${previous} ${tip.id}` : tip.id);

	let timer: ReturnType<typeof setTimeout> | undefined;
	let pointerDown = false;

	const hide = () => {
		clearTimeout(timer);
		tip.removeAttribute('data-open');
		window.removeEventListener('keydown', onKey, true);
		window.removeEventListener('scroll', hide, true);
		window.removeEventListener('resize', hide);
	};
	const open = () => {
		if (opts.text === '') return;
		tip.setAttribute('data-open', 'true');
		place(tip, node, opts.placement);
		window.addEventListener('keydown', onKey, true);
		window.addEventListener('scroll', hide, true);
		window.addEventListener('resize', hide);
	};
	// Escape also reaches a menu around the control, so one press closes both.
	const onKey = (event: KeyboardEvent) => {
		if (event.key === 'Escape') hide();
	};
	const onEnter = () => {
		clearTimeout(timer);
		if (tip.hasAttribute('data-open')) return;
		timer = setTimeout(open, opts.delay);
	};
	// The pointer may cross onto the tooltip to read it, so leaving waits a moment.
	const onLeave = () => {
		clearTimeout(timer);
		timer = setTimeout(hide, HOVER_GRACE_MS);
	};
	const onFocus = () => {
		clearTimeout(timer);
		if (!pointerDown) open();
	};
	const onDown = () => {
		pointerDown = true;
	};
	const onUp = () => {
		pointerDown = false;
	};
	const onClick = () => {
		pointerDown = false;
		hide();
	};

	node.addEventListener('mouseenter', onEnter);
	node.addEventListener('mouseleave', onLeave);
	node.addEventListener('focus', onFocus);
	node.addEventListener('blur', hide);
	node.addEventListener('mousedown', onDown);
	node.addEventListener('mouseup', onUp);
	node.addEventListener('click', onClick);
	tip.addEventListener('mouseenter', () => clearTimeout(timer));
	tip.addEventListener('mouseleave', onLeave);

	return {
		update(next: TooltipParam) {
			opts = normalize(next);
			fillText(tip, opts.text);
			if (tip.hasAttribute('data-open')) place(tip, node, opts.placement);
		},
		destroy() {
			hide();
			node.removeEventListener('mouseenter', onEnter);
			node.removeEventListener('mouseleave', onLeave);
			node.removeEventListener('focus', onFocus);
			node.removeEventListener('blur', hide);
			node.removeEventListener('mousedown', onDown);
			node.removeEventListener('mouseup', onUp);
			node.removeEventListener('click', onClick);
			if (previous) node.setAttribute('aria-describedby', previous);
			else node.removeAttribute('aria-describedby');
			tip.remove();
		}
	};
}
