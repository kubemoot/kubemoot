import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import { forbidden, subjectTargetAllowed } from '$lib/server/scope';

/**
 * Publishes a message to a NATS subject via the server-side connection.
 *
 * Usage: POST /api/nats/publish
 * Body: { "subject": "kubemoot.chat.<namespace>.admin", "data": "..." }
 */
export const POST: RequestHandler = async ({ request }) => {
	try {
		const { subject, data } = await request.json();

		if (!subject || !data) {
			return json({ error: 'subject and data are required' }, { status: 400 });
		}

		if (!(await subjectTargetAllowed(subject))) return forbidden('subject');

		const nc = await getNatsConnection();
		nc.publish(subject, sc.encode(typeof data === 'string' ? data : JSON.stringify(data)));

		return json({ success: true });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to publish';
		return json({ error: message }, { status: 500 });
	}
};
