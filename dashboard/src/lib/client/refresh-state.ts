// Loading rules shared by every page that fetches on a timer.
//
// A page shows its loading indicator only while it has nothing to show. Once
// data has arrived, a refresh runs in the background and the new values replace
// the old in place; a failed refresh keeps the last data on screen.

/** The loading flag to set when a fetch starts: true only when no data is held yet. */
export function loadingOnStart(hasData: boolean): boolean {
	return !hasData;
}

/**
 * The error to show after a failed fetch. A refresh that fails while data is
 * held returns null so the last data stays on screen; the first load that fails
 * returns the message.
 */
export function errorOnFailure(hasData: boolean, failure: unknown, fallback: string): string | null {
	if (hasData) return null;
	return failure instanceof Error ? failure.message : fallback;
}
