import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import { isReadOnly, isScoped } from '$lib/server/mode';
import { forbidden } from '$lib/server/scope';

const BUCKET = 'kubemoot_pinned_threads';

// A read-only dashboard binds to the bucket and never creates it.
async function getKv() {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	return await js.views.kv(BUCKET, { history: 1, bindOnly: isReadOnly() });
}

// Pins are keyed by thread id alone, so they carry no namespace to filter by. A
// namespace-scoped dashboard serves none and accepts none.
export const GET: RequestHandler = async () => {
	if (isScoped()) return json({ pinned: {} });
	try {
		const kv = await getKv();
		const pinned: Record<string, { pinnedAt: string; note?: string }> = {};
		const keys = await kv.keys();
		for await (const key of keys) {
			const entry = await kv.get(key);
			if (entry) {
				try {
					pinned[key] = JSON.parse(sc.decode(entry.value));
				} catch {
					pinned[key] = { pinnedAt: new Date(entry.created).toISOString() };
				}
			}
		}
		return json({ pinned });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to list pinned threads';
		return json({ error: message }, { status: 500 });
	}
};

export const POST: RequestHandler = async ({ request }) => {
	if (isScoped()) return forbidden('pinning');
	try {
		const { threadId, note } = await request.json();
		if (!threadId) {
			return json({ error: 'threadId is required' }, { status: 400 });
		}
		const kv = await getKv();
		const payload = JSON.stringify({ pinnedAt: new Date().toISOString(), note: note ?? null });
		await kv.put(threadId, sc.encode(payload));
		return json({ success: true, threadId });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to pin thread';
		return json({ error: message }, { status: 500 });
	}
};

export const DELETE: RequestHandler = async ({ request }) => {
	if (isScoped()) return forbidden('unpinning');
	try {
		const { threadId } = await request.json();
		if (!threadId) {
			return json({ error: 'threadId is required' }, { status: 400 });
		}
		const kv = await getKv();
		await kv.delete(threadId);
		return json({ success: true, threadId });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to unpin thread';
		return json({ error: message }, { status: 500 });
	}
};
