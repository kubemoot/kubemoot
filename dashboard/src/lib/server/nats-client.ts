import { connect, type NatsConnection, StringCodec } from 'nats';
import { describeNatsEnv, natsConnectionOptions } from './nats-options.js';

const NATS_URL = process.env.NATS_URL || 'nats://nats.nats.svc.cluster.local:4222';

let nc: NatsConnection | null = null;
let connecting: Promise<NatsConnection> | null = null;

/**
 * Opens a connection to url with the TLS and credential options in env (NATS_CA_FILE,
 * NATS_CLIENT_CERT_FILE, NATS_CLIENT_KEY_FILE, NATS_CREDS_FILE, NATS_USER and NATS_PASSWORD, or
 * NATS_TOKEN). A failure names the URL and the options in play.
 */
export async function connectTo(
	url: string,
	env: Record<string, string | undefined>,
	extra: { maxReconnectAttempts?: number; timeout?: number } = {}
): Promise<NatsConnection> {
	try {
		return await connect({
			servers: url,
			...(extra.timeout === undefined ? {} : { timeout: extra.timeout }),
			maxReconnectAttempts: extra.maxReconnectAttempts ?? -1, // Infinite retries
			reconnectTimeWait: 2000, // 2s between retries
			reconnectJitter: 1000, // Add jitter
			...natsConnectionOptions(env, url)
		});
	} catch (err) {
		const reason = err instanceof Error ? err.message : String(err);
		throw new Error(`connecting to NATS at ${url} (${describeNatsEnv(env)}): ${reason}`, { cause: err });
	}
}

/**
 * Get a shared NATS connection (server-side only).
 * Uses TCP connection to NATS (not WebSocket) since we're running inside the cluster.
 */
export async function getNatsConnection(): Promise<NatsConnection> {
	if (nc && !nc.isClosed()) {
		return nc;
	}

	// Prevent concurrent connection attempts
	if (connecting) {
		return connecting;
	}

	connecting = connectTo(NATS_URL, process.env)
		.then((conn) => {
			nc = conn;
			connecting = null;
			console.log(`Connected to NATS at ${NATS_URL}`);

			// Handle permanent close - null out connection so next call reconnects
			conn.closed().then(() => {
				console.log('NATS connection closed permanently');
				if (nc === conn) nc = null;
			});

			return conn;
		})
		.catch((err) => {
			connecting = null;
			throw err;
		});

	return connecting;
}

export const sc = StringCodec();
