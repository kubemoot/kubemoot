// The small status indicator in an open discussion's header: what state the conversation
// is in, and a tooltip that also carries the crew version and who started it.
import { crewVersionTooltip } from '#lib/crew-version.js';

export type StatusKind = 'working' | 'answered' | 'failed' | 'stopped';

export interface StatusIcon {
	kind: StatusKind;
	/** The accessible name. */
	label: string;
	/** The tooltip: the label plus the crew version and the starter. */
	tooltip: string;
}

const LABELS: Record<StatusKind, string> = {
	working: 'Active',
	answered: 'Answered',
	failed: 'Failed, no answer',
	stopped: 'Stopped'
};

/** The icon's state, accessible name and tooltip for a thread. */
export function statusIcon(
	kind: StatusKind,
	thread?: { crewVersion?: string; startedBy?: string }
): StatusIcon {
	const label = LABELS[kind];
	const extra = [
		crewVersionTooltip(thread?.crewVersion),
		thread?.startedBy === 'human' ? 'Started by a person' : ''
	].filter(Boolean);
	return { kind, label, tooltip: [label, ...extra].join('. ') };
}
