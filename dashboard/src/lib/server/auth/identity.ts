import { AsyncLocalStorage } from 'node:async_hooks';
import type { BearerToken } from './bearer-token.js';

/** Who a request acts as: a signed-in person with their own token, or an anonymous viewer. */
export type RequestIdentity =
	| { kind: 'caller'; username: string; token: BearerToken; ended: AbortSignal }
	| { kind: 'anonymous' };

const storage = new AsyncLocalStorage<RequestIdentity>();

/** Runs fn with identity as the request identity; everything it awaits sees the same one. */
export function runWithIdentity<T>(identity: RequestIdentity, fn: () => T): T {
	return storage.run(identity, fn);
}

/** The identity of the request being served, or undefined outside a request. */
export function currentIdentity(): RequestIdentity | undefined {
	return storage.getStore();
}

/** Aborts when the caller's session ends (sign-out, expiry), so a long-lived stream can stop with it. */
export function callerEnded(): AbortSignal | undefined {
	const identity = storage.getStore();
	return identity?.kind === 'caller' ? identity.ended : undefined;
}

/** The signed-in caller's token, or undefined for an anonymous viewer or outside a request. */
export function callerToken(): BearerToken | undefined {
	const identity = storage.getStore();
	return identity?.kind === 'caller' ? identity.token : undefined;
}
