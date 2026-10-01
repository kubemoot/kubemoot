// Model load/unload actions the ModelProviderCard shows as in flight until the
// ModelProvider status catches up (or the give-up TTL runs out).

export interface PendingAction {
	action: 'load' | 'unload';
	startedAt: number;
}

export const PENDING_TTL_MS = 60_000;

/** True once the action is done: the model's loaded state matches it, or it started before the cutoff. */
export function isSettled(
	model: string,
	p: PendingAction,
	loaded: Set<string>,
	cutoff: number
): boolean {
	if (p.startedAt < cutoff) return true;
	return p.action === 'load' ? loaded.has(model) : !loaded.has(model);
}

/** The actions still in flight, or null when none settled (so callers can skip a state write). */
export function unsettledActions(
	pending: Record<string, PendingAction>,
	loaded: Set<string>,
	cutoff: number
): Record<string, PendingAction> | null {
	const entries = Object.entries(pending);
	const open = entries.filter(([model, p]) => !isSettled(model, p, loaded, cutoff));
	return open.length === entries.length ? null : Object.fromEntries(open);
}
