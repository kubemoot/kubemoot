import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { deleteCrewFitnessSuite } from '$lib/server/k8s';

/**
 * DELETE /api/kubemoot/crewfitnesssuites/{namespace}/{name}
 *
 * Removes a fitness suite run. Deleting the CrewFitnessSuite cascades its owned
 * CrewFitness children (owner refs) and triggers the operator's finalizer
 * (kubemoot.ai/fitness-artifacts) to purge the run's NATS Object Store artifacts
 * under "{namespace}/{name}/": the XLSX report, per-iteration transcripts, and the
 * deferred-score sidecar.
 */
export const DELETE: RequestHandler = async ({ params }) => {
	const { namespace, name } = params;
	if (!namespace || !name) {
		throw error(400, 'namespace and name are required');
	}
	try {
		await deleteCrewFitnessSuite(namespace, name);
		return json({ deleted: `${namespace}/${name}` });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'delete failed';
		return json({ error: message }, { status: 500 });
	}
};
