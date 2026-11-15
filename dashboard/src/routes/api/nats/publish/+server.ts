import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '#lib/server/nats-client.js';
import { forbidden, subjectTargetAllowed } from '#lib/server/scope.js';

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
			return Response.json({ error: 'subject and data are required' }, { status: 400 });
		}

		if (!(await subjectTargetAllowed(subject))) return forbidden('subject');

		const nc = await getNatsConnection();
		nc.publish(subject, sc.encode(typeof data === 'string' ? data : JSON.stringify(data)));
		// Core publish, then flush: a success answer means the NATS server has the message. The
		// subject is captured by the discussion stream on the server side; this does not wait for that.
		await nc.flush();

		return Response.json({ success: true });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to publish';
		return Response.json({ error: message }, { status: 500 });
	}
};
