import * as k8s from '@kubernetes/client-node';
import { getKubeConfig } from './client.js';
import { KUBEMOOT_CRDS } from './kubemoot-crds.js';
import { describeError } from '$lib/text-utils';

const GROUP = 'kubemoot.ai';
const VERSION = 'v1alpha1';

export type CrdPlural = keyof typeof KUBEMOOT_CRDS;

export function isWatchableCrd(plural: string): plural is CrdPlural {
	return Object.prototype.hasOwnProperty.call(KUBEMOOT_CRDS, plural);
}

/**
 * Build an SSE Response that relays a k8s WATCH on one or more kubemoot CRDs to
 * the browser. Each event is `{ kind, type: ADDED|MODIFIED|DELETED, object }`;
 * a `{ type: "synced" }` marker is sent once all watches are established, and a
 * `: hb` comment keeps the connection alive. Watches are aborted on client
 * disconnect. Shared by the generic /watch/[plural] endpoint and the combined
 * fitness watch.
 *
 * This is the push alternative to polling: one watch per resource streams only
 * actual changes — realtime, flicker-free (the browser diffs keyed rows), and
 * near-zero idle load.
 */
export function crdWatchResponse(plurals: CrdPlural[], namespace: string): Response {
	const kc = getKubeConfig();
	const watch = new k8s.Watch(kc);
	const enc = new TextEncoder();
	const aborters: AbortController[] = [];
	let heartbeat: ReturnType<typeof setInterval> | undefined;
	let closed = false;

	const pathFor = (plural: string) =>
		namespace
			? `/apis/${GROUP}/${VERSION}/namespaces/${namespace}/${plural}`
			: `/apis/${GROUP}/${VERSION}/${plural}`;

	const cleanup = () => {
		closed = true;
		if (heartbeat) clearInterval(heartbeat);
		for (const ac of aborters) {
			try {
				ac.abort();
			} catch {
				/* already aborted */
			}
		}
	};

	const stream = new ReadableStream({
		async start(controller) {
			const send = (obj: unknown) => {
				if (closed) return;
				try {
					controller.enqueue(enc.encode(`data: ${JSON.stringify(obj)}\n\n`));
				} catch {
					cleanup();
				}
			};

			// A k8s watch ends after the API server's timeout (minutes) or on a
			// transient error. If we don't RESTART it, the SSE stays open (heartbeats)
			// but no further ADDED/MODIFIED/DELETED events arrive — the page silently
			// freezes at the last state until a manual refresh. So re-establish the
			// watch whenever it ends; restarting re-lists current objects (ADDED),
			// which also heals any change missed during the gap.
			const startWatch = async (plural: CrdPlural) => {
				if (closed) return;
				const kind = KUBEMOOT_CRDS[plural].kind;
				try {
					const ac = await watch.watch(
						pathFor(plural),
						{},
						(type: string, obj: unknown) => send({ kind, type, object: obj }),
						(err: unknown) => {
							if (err) send({ kind, type: 'ERROR', error: describeError(err) });
							if (!closed) setTimeout(() => void startWatch(plural), 1000);
						}
					);
					aborters.push(ac);
				} catch (e) {
					send({ kind, type: 'ERROR', error: e instanceof Error ? e.message : String(e) });
					if (!closed) setTimeout(() => void startWatch(plural), 2000);
				}
			};
			for (const plural of plurals) {
				await startWatch(plural);
			}
			send({ type: 'synced' });

			heartbeat = setInterval(() => {
				if (closed) return;
				try {
					controller.enqueue(enc.encode(': hb\n\n'));
				} catch {
					cleanup();
				}
			}, 25000);
		},
		cancel() {
			cleanup();
		}
	});

	return new Response(stream, {
		headers: {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			Connection: 'keep-alive'
		}
	});
}
