import type { RequestHandler } from './$types';
import { readConfigMap } from '#lib/server/k8s/index.js';
import { isToken } from '#lib/crewScope.js';
import { guardNamespace } from '#lib/server/scope.js';

/**
 * Returns the agent's assembled system prompt from its `<agent>-policy`
 * ConfigMap. The operator concatenates referenced PromptModule content
 * into `system.txt` on every Agent reconcile.
 *
 * Other keys in the ConfigMap (e.g. application.properties for the
 * agent-runtime SmallRye config) are also returned for inspection.
 */
export const GET: RequestHandler = async ({ params, url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	const { name } = params;
	if (!isToken(name)) {
		return Response.json({ error: 'name is required' }, { status: 400 });
	}

	const policyName = `${name}-policy`;
	try {
		const cm = await readConfigMap(namespace, policyName);
		return Response.json({
			configMapName: policyName,
			namespace,
			systemPrompt: cm.data?.['system.txt'] ?? null,
			keys: Object.keys(cm.data ?? {}),
			data: cm.data ?? {}
		});
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to read agent policy ConfigMap';
		return Response.json({ error: message, configMapName: policyName, namespace }, { status: 404 });
	}
};
