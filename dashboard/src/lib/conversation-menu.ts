// The entries of the open conversation's actions menu, and when each applies. Pure, so a test
// can say which entries a conversation in each state offers.

import type { MenuEntry } from '#lib/components/common/ActionMenu.svelte';
import type { CrewScope } from '#lib/crewScope.js';
import type { Control } from '#lib/conversation-actions-client.js';
import type { RunState } from '#lib/conversation-actions.js';

export interface MenuContext {
	scope: CrewScope | null;
	conversationId: string | undefined;
	run: RunState;
	showDetails: boolean;
	downloadHref: string | null;
	discussionHref: string;
	ontoggledetails: () => void;
	copyConversation: () => void;
	copyIdAndQuestion: () => void;
	control: (control: Control) => void;
	remove: () => void;
}

const CONTROL_LABELS: Record<Control, string> = { stop: 'Stop', pause: 'Pause', resume: 'Resume' };

/** A question that still waits in the queue is cancelled, not stopped, and cannot be paused. */
function controlsOf(run: RunState): Control[] {
	if (run.queuedOnly) return ['stop'];
	return [run.paused ? 'resume' : 'pause', 'stop'];
}

const separator: MenuEntry = { kind: 'separator' };

function viewEntries(c: MenuContext): MenuEntry[] {
	return [
		{ kind: 'check', label: 'Show answer details', checked: c.showDetails, onselect: c.ontoggledetails },
		separator,
		{ kind: 'item', label: 'Copy conversation', onselect: c.copyConversation }
	];
}

function shareEntries(c: MenuContext): MenuEntry[] {
	const entries: MenuEntry[] = [];
	if (c.conversationId) entries.push({ kind: 'item', label: 'Copy conversation id with the first question', onselect: c.copyIdAndQuestion });
	if (c.downloadHref) entries.push({ kind: 'link', label: 'Download conversation', href: c.downloadHref, download: true });
	entries.push({ kind: 'link', label: 'View associated moot', href: c.discussionHref });
	return entries;
}

function controlEntries(c: MenuContext): MenuEntry[] {
	if (!c.conversationId || !c.scope || !c.run.running) return [];
	const label = (control: Control) => (control === 'stop' && c.run.queuedOnly ? 'Cancel' : CONTROL_LABELS[control]);
	return [separator, ...controlsOf(c.run).map((control): MenuEntry => ({ kind: 'item', label: label(control), onselect: () => c.control(control) }))];
}

function deleteEntries(c: MenuContext): MenuEntry[] {
	if (!c.conversationId || !c.scope) return [];
	return [separator, { kind: 'item', label: 'Delete conversation', danger: true, onselect: c.remove }];
}

/** The menu of an open conversation: what applies to its state, in the order a person reaches for it. */
export function conversationMenu(c: MenuContext): MenuEntry[] {
	return [...viewEntries(c), ...shareEntries(c), ...controlEntries(c), ...deleteEntries(c)];
}
