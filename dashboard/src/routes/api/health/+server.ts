import type { RequestHandler } from './$types';
import { checkConnection } from '#lib/server/k8s/index.js';

export const GET: RequestHandler = async () => {
	const k8sStatus = await checkConnection();

	return Response.json({
		status: k8sStatus.connected ? 'healthy' : 'unhealthy',
		timestamp: new Date().toISOString(),
		kubernetes: {
			connected: k8sStatus.connected,
			version: k8sStatus.version,
			error: k8sStatus.error
		}
	});
};
