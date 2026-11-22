// The icon bar that appears over a message or a list row on hover or keyboard focus.

import type { HoverId } from '#lib/action-definitions.js';

export type HoverIcon = 'copy' | 'edit' | 'delete' | 'id';

export interface HoverAction {
	/** Names the action for assistive technology; also the accessible name of the icon button. */
	label: string;
	/** Which shared tooltip the icon carries. */
	id: HoverId;
	icon: HoverIcon;
	/** Runs the action. A copy that throws (the browser refused) does not show the done tick. */
	run: () => void | Promise<void>;
	danger?: boolean;
}

/** The icons that show a tick for a moment once they have run. */
export function showsTick(icon: HoverIcon): boolean {
	return icon === 'copy' || icon === 'id';
}
