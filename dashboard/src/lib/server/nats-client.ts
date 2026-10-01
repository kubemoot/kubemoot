import { connect, type NatsConnection, StringCodec } from 'nats';

const NATS_URL = process.env.NATS_URL || 'nats://nats.nats.svc.cluster.local:4222';

let nc: NatsConnection | null = null;
let connecting: Promise<NatsConnection> | null = null;

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

	connecting = connect({
		servers: NATS_URL,
		maxReconnectAttempts: -1, // Infinite retries
		reconnectTimeWait: 2000, // 2s between retries
		reconnectJitter: 1000 // Add jitter
	})
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
