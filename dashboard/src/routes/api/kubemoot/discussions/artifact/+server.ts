import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { readDiscussionArtifact } from '$lib/server/nats-object-store';

/**
 * GET /api/kubemoot/discussions/artifact?key=...
 *
 * Read-only download of a spilled discussion artifact: the FULL agent
 * contribution behind an `[ARTIFACT key=...]` marker in a finding or synthesis.
 * The object lives in the NATS object store bucket `kubemoot_discussion_artifacts`,
 * written by the agent-runtime spill (DiscussionSubscriber) at the thread-scoped
 * key `{crew}/{threadId}/{agent}/{signal}-{uuid}`.
 *
 * The key is VALIDATED to that exact 4-segment shape (mirrors the guard in the
 * fitness transcript endpoint): no `..`, no leading/trailing slash, each segment
 * a safe `[A-Za-z0-9._-]+`. This rejects path traversal and any crafted key that
 * tries to read outside the discussion-artifact namespace.
 *
 *   200 + the artifact bytes (Content-Disposition attachment) on success
 *   400 when the key is missing or fails the shape guard
 *   404 when the object is absent (TTL-pruned, GC-reaped, or never written)
 *   500 when NATS itself is unreachable
 */

// {crew}/{threadId}/{agent}/{signal}-{uuid}. Four non-empty segments of safe
// characters; the trailing segment is `{signal}-{uuid}`. Rejects `..` implicitly
// (a dot run is only matched within a segment, never as a path part) but we also
// guard `..` explicitly below for defense in depth.
const KEY_SHAPE = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/;

/** Build a safe download filename from the validated key: `{agent}-{signal}.txt`. */
function filenameFor(key: string): string {
	const parts = key.split('/');
	const agent = parts[2] ?? 'artifact';
	const signal = (parts[3] ?? 'data').split('-')[0] || 'data';
	const safe = `${agent}-${signal}`.replace(/[^A-Za-z0-9._-]/g, '_');
	return `${safe}.txt`;
}

export const GET: RequestHandler = async ({ url }) => {
	const key = url.searchParams.get('key');
	if (!key) {
		return json({ error: 'key required' }, { status: 400 });
	}
	if (key.includes('..') || !KEY_SHAPE.test(key)) {
		return json({ error: 'invalid artifact key' }, { status: 400 });
	}
	try {
		const bytes = await readDiscussionArtifact(key);
		if (!bytes) {
			return json(
				{ error: 'artifact not found (TTL-pruned, GC-reaped, or never written)' },
				{ status: 404 }
			);
		}
		const buf = Buffer.from(bytes);
		return new Response(buf, {
			status: 200,
			headers: {
				'Content-Type': 'text/plain; charset=utf-8',
				'Content-Disposition': `attachment; filename="${filenameFor(key)}"`,
				'Content-Length': String(buf.byteLength),
				'Cache-Control': 'no-store'
			}
		});
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to read artifact';
		return json({ error: message }, { status: 500 });
	}
};
