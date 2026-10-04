import { error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { deleteCrewFitnessSuite, patchCrewFitnessSuiteSpec } from '#lib/server/k8s/index.js';
import { suiteActionPatch } from '#lib/fitness-suite-controls.js';
import { guardNamespace } from '#lib/server/scope.js';

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
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	if (!namespace || !name) {
		throw error(400, 'namespace and name are required');
	}
	try {
		await deleteCrewFitnessSuite(namespace, name);
		return Response.json({ deleted: `${namespace}/${name}` });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'delete failed';
		return Response.json({ error: message }, { status: 500 });
	}
};

/**
 * PATCH /api/kubemoot/crewfitnesssuites/{namespace}/{name}
 * Body: { "action": "pause" | "resume" | "stop" }
 *
 * Pause sets spec.suspend=true (the running iteration finishes, no new one
 * starts, phase becomes Paused). Resume sets spec.suspend=false. Stop sets
 * spec.cancel=true (the in-flight iteration is deleted, phase becomes Cancelled,
 * a partial XLSX is written). The operator does the work; this only patches spec.
 */
export const PATCH: RequestHandler = async ({ params, request }) => {
	const { namespace, name } = params;
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	if (!namespace || !name) {
		throw error(400, 'namespace and name are required');
	}
	const body = await request.json().catch(() => ({}));
	const patch = suiteActionPatch(body?.action);
	if (!patch) {
		return Response.json({ error: 'action must be one of pause, resume, stop' }, { status: 400 });
	}
	try {
		await patchCrewFitnessSuiteSpec(namespace, name, patch);
		return Response.json({ suite: `${namespace}/${name}`, action: body.action });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'patch failed';
		return Response.json({ error: message }, { status: 500 });
	}
};
