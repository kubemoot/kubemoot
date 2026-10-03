import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { readDiscussionArtifact } from '$lib/server/nats-object-store';
import { parseArtifactKey, type ArtifactKey } from '$lib/crewScope';
import { guardKeyNamespace } from '$lib/server/scope';

/**
 * GET /api/kubemoot/discussions/artifact?key=...
 *
 * Read-only download of a spilled discussion artifact: the FULL agent
 * contribution behind an `[ARTIFACT key=...]` marker in a finding or synthesis.
 * The object lives in the NATS object store bucket `kubemoot_discussion_artifacts`,
 * written by the agent-runtime spill (DiscussionSubscriber) at the namespaced,
 * thread-scoped key `{namespace}/{crew}/{threadId}/{agent}/{signal}-{uuid}`.
 *
 * The key is VALIDATED to that exact 5-segment shape by parseArtifactKey: no
 * `..`, no leading or trailing slash, a DNS-label namespace, and each other
 * segment a safe `[A-Za-z0-9._-]+`. This rejects path traversal and any crafted
 * key that tries to read outside the discussion-artifact namespace.
 *
 *   200 + the artifact bytes (Content-Disposition attachment) on success
 *   400 when the key is missing or fails the shape guard
 *   404 when the object is absent (TTL-pruned, GC-reaped, or never written)
 *   500 when NATS itself is unreachable
 */

/** Build a safe download filename from the validated key: `{agent}-{signal}.txt`. */
function filenameFor(parsed: ArtifactKey): string {
	const signal = parsed.name.split('-')[0] || 'data';
	const safe = `${parsed.agent}-${signal}`.replaceAll(/[^A-Za-z0-9._-]/g, '_');
	return `${safe}.txt`;
}

export const GET: RequestHandler = async ({ url }) => {
	const key = url.searchParams.get('key');
	if (!key) {
		return json({ error: 'key required' }, { status: 400 });
	}
	const parsed = parseArtifactKey(key);
	if (!parsed) {
		return json({ error: 'invalid artifact key' }, { status: 400 });
	}
	const denied = await guardKeyNamespace(parsed);
	if (denied) return denied;
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
				'Content-Disposition': `attachment; filename="${filenameFor(parsed)}"`,
				'Content-Length': String(buf.byteLength),
				'Cache-Control': 'no-store'
			}
		});
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to read artifact';
		return json({ error: message }, { status: 500 });
	}
};
