// See https://svelte.dev/docs/kit/types#app.d.ts
declare global {
	namespace App {
		interface Locals {
			/** The signed-in person, or null for a visitor who has not signed in. */
			user: { username: string } | null;
			/** The session cookie value, when the request carried a live session. */
			sessionId?: string;
		}
		interface Platform {
			/** The Node request, from adapter-node; its socket says whether the connection is TLS. */
			req: import('node:http').IncomingMessage;
		}
		interface PageData {
			scoped: boolean;
			anonymousReadOnly: boolean;
			user: { username: string } | null;
		}
	}
}

export {};
