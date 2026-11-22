/**
 * A limit on sign-in attempts, so the sign-in page cannot be used to flood the API server with
 * TokenReviews: a number per client address and a larger number for the whole dashboard, in one
 * window. Kept in memory.
 */
export const WINDOW_MS = 60_000;
export const PER_CLIENT = 10;
export const IN_TOTAL = 120;

const attempts = new Map<string, number[]>();

function recent(key: string, now: number): number[] {
	const fresh = (attempts.get(key) ?? []).filter((t) => now - t < WINDOW_MS);
	if (fresh.length > 0) attempts.set(key, fresh);
	else attempts.delete(key);
	return fresh;
}

/** Records one attempt and says whether it may go ahead. */
export function allowSignInAttempt(client: string, now = Date.now()): boolean {
	const mine = recent(client, now);
	const all = recent('*', now);
	if (mine.length >= PER_CLIENT || all.length >= IN_TOTAL) return false;
	attempts.set(client, [...mine, now]);
	attempts.set('*', [...all, now]);
	return true;
}

/** Test helper. */
export function resetSignInAttempts(): void {
	attempts.clear();
}
