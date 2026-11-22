import { callerToken } from './auth/identity.js';

interface Held<T> {
	at: number;
	value: Promise<T>;
}

// Shares one read among callers: while a result is younger than `ttlMs`, or still being
// read, every caller gets it. Several open pages then cost NATS one read per interval.
// A read made with a signed-in caller's token is shared only with that caller's own requests,
// never with another person, whose rights may differ.
export function cachedRead<T>(ttlMs: number, read: () => Promise<T>): () => Promise<T> {
	let shared: Held<T> | null = null;
	const perCaller = new WeakMap<object, Held<T>>();

	// The place this request's result is kept: the caller's own, or the shared one.
	const slot = () => {
		const owner = callerToken();
		return {
			get: () => (owner ? perCaller.get(owner) : (shared ?? undefined)),
			set: (held: Held<T> | null) => {
				if (!owner) shared = held;
				else if (held) perCaller.set(owner, held);
				else perCaller.delete(owner);
			}
		};
	};

	return () => {
		const place = slot();
		const held = place.get();
		const now = Date.now();
		if (held && now - held.at < ttlMs) return held.value;
		const value = read();
		const fresh: Held<T> = { at: now, value };
		place.set(fresh);
		value.catch(() => {
			if (place.get() === fresh) place.set(null);
		});
		return value;
	};
}
