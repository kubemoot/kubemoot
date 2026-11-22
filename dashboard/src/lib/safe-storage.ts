// The browser's localStorage for a per-viewer convenience (a remembered filter, role or collapsed
// list). Storage can be missing or refuse (a private window, blocked site data, a full quota):
// every call then does nothing and the page uses its default.

function store(): Storage | null {
	try {
		return typeof localStorage === 'undefined' ? null : localStorage;
	} catch {
		return null;
	}
}

/** The stored text, or null when none is kept or storage is unavailable. */
export function readItem(key: string): string | null {
	try {
		return store()?.getItem(key) ?? null;
	} catch {
		return null;
	}
}

/** Keeps the text; does nothing when storage is unavailable or full. */
export function writeItem(key: string, value: string): void {
	try {
		store()?.setItem(key, value);
	} catch {
		// The choice still applies for this visit; it is just not remembered.
	}
}
