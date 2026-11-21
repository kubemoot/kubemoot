// Shares one read among callers: while a result is younger than `ttlMs`, or still being
// read, every caller gets it. Several open pages then cost NATS one read per interval.
export function cachedRead<T>(ttlMs: number, read: () => Promise<T>): () => Promise<T> {
	let held: { at: number; value: Promise<T> } | null = null;
	return () => {
		const now = Date.now();
		if (held && now - held.at < ttlMs) return held.value;
		const value = read();
		held = { at: now, value };
		value.catch(() => {
			if (held?.value === value) held = null;
		});
		return value;
	};
}
