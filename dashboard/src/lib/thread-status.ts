// What a thread's status is called wherever a list or a header shows it, so the Moot and the
// Conversations page say the same word for the same state.

/** The human label of a raw thread status (open is Active, synthesized is Answered). */
export function statusLabel(status: string): string {
	if (status === 'open') return 'Active';
	if (status === 'synthesized') return 'Answered';
	return 'Closed';
}
