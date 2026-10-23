// Wording and rules for the actions of the Discussions view, kept in one place so
// the same action reads the same everywhere it appears.

import type { CrewScope } from '#lib/crewScope.js';

export const DELETE_TOOLTIP =
	'Delete this discussion: removes it from the timeline and purges its messages from the bus. This cannot be undone. A pin does not prevent it.';

export const PIN_TOOLTIP =
	'Pin this discussion: marks it as one you want to find again. The pin is saved on the bus, so it stays after a reload and shows in other browsers.';
export const UNPIN_TOOLTIP = 'Unpin this discussion: removes the mark that flags it as one to find again.';

export const COPY_THREAD_TOOLTIP =
	'Copy the whole discussion as Markdown to the clipboard, to paste into a ticket or a note.';
export const SPAN_GRAPH_TOOLTIP =
	'Show or hide the span graph: one bar per agent from its first to its last message, to see who was slow or waiting.';
export const RESUME_TOOLTIP = 'Resume this paused discussion: restarts the settle timer so the coordinator can answer.';
export const PAUSE_TOOLTIP =
	'Pause this discussion: stops the settle timer so the coordinator waits while you read or add a message.';
export const STOP_TOOLTIP =
	'Stop this discussion now: the coordinator writes its answer from the signals it has so far.';
export const REPLAY_TOOLTIP = 'Replay this question: starts a new discussion with the same question and channel.';
export const COPY_ID_TOOLTIP = 'Copy the thread ID, to find this discussion in logs or on the bus.';

export const NEW_DISCUSSION_NEEDS_CREW =
	'Pick one crew in the top bar first: a discussion is asked of a single crew, and "All crews" has no target.';
export const NEW_DISCUSSION_TOOLTIP = 'Ask the selected crew a question and watch its agents discuss it.';

/** Whether a new discussion has a target: exactly one crew is selected. */
export function canStartDiscussion(scope: CrewScope | null): boolean {
	return scope !== null;
}

/** The tooltip of the New discussion button, saying why it is disabled when it is. */
export function newDiscussionTooltip(scope: CrewScope | null): string {
	return canStartDiscussion(scope) ? NEW_DISCUSSION_TOOLTIP : NEW_DISCUSSION_NEEDS_CREW;
}

/** The tooltip of the Pin button for the thread's current state. */
export function pinTooltip(pinned: boolean): string {
	return pinned ? UNPIN_TOOLTIP : PIN_TOOLTIP;
}
