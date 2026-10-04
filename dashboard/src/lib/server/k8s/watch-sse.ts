import * as k8s from '@kubernetes/client-node';
import { getKubeConfig } from './client.js';
import { KUBEMOOT_CRDS } from './kubemoot-crds.js';
import { describeError } from '#lib/text-utils.js';
import { sseResponse } from '../sse.js';

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
 * heartbeat comment keeps the connection alive. Watches are aborted on client
 * disconnect. Shared by the generic /watch/[plural] endpoint and the combined
 * fitness watch. `accept` drops the objects a scoped dashboard may not show.
 *
 * This is the push alternative to polling: one watch per resource streams only
 * actual changes: realtime, flicker-free (the browser diffs keyed rows), and
 * near-zero idle load.
 */
export function crdWatchResponse(
	plurals: CrdPlural[],
	namespace: string,
	accept: (obj: unknown) => boolean = () => true
): Response {
	const kc = getKubeConfig();
	const watch = new k8s.Watch(kc);
	// One live watch per plural; a restart replaces the controller of the watch that ended.
	const aborters = new Map<CrdPlural, AbortController>();

	const pathFor = (plural: string) =>
		namespace
			? `/apis/${GROUP}/${VERSION}/namespaces/${namespace}/${plural}`
			: `/apis/${GROUP}/${VERSION}/${plural}`;

	return sseResponse(async (sink) => {
		// A k8s watch ends after the API server's timeout (minutes) or on a
		// transient error. If we don't RESTART it, the SSE stays open (heartbeats)
		// but no further ADDED/MODIFIED/DELETED events arrive - the page silently
		// freezes at the last state until a manual refresh. So re-establish the
		// watch whenever it ends; restarting re-lists current objects (ADDED),
		// which also heals any change missed during the gap.
		const startWatch = async (plural: CrdPlural) => {
			if (sink.closed) return;
			const kind = KUBEMOOT_CRDS[plural].kind;
			try {
				const ac = await watch.watch(
					pathFor(plural),
					{},
					(type: string, obj: unknown) => {
						if (accept(obj)) sink.data({ kind, type, object: obj });
					},
					(err: unknown) => {
						if (err) sink.data({ kind, type: 'ERROR', error: describeError(err) });
						if (!sink.closed) setTimeout(() => void startWatch(plural), 1000);
					}
				);
				aborters.set(plural, ac);
				if (sink.closed) ac.abort();
			} catch (e) {
				sink.data({ kind, type: 'ERROR', error: e instanceof Error ? e.message : String(e) });
				if (!sink.closed) setTimeout(() => void startWatch(plural), 2000);
			}
		};
		for (const plural of plurals) {
			await startWatch(plural);
		}
		sink.data({ type: 'synced' });

		return () => {
			for (const ac of aborters.values()) ac.abort();
		};
	}, { heartbeatMs: 25_000 });
}
