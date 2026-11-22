import { getAppsApi } from './client.js';

/** How many instances of a workload run, as the Deployment itself reports them. */
export interface ReplicaCounts {
	/** The Deployment's own target: spec.replicas, which is 0 while a scale-to-zero agent idles. */
	desired: number;
	/** Instances that are ready to serve. */
	ready: number;
}

/**
 * The replica counts of the Deployment the operator makes for an agent (same name, same
 * namespace). The Agent resource does not carry them, so this is the one source of the
 * numbers the page shows. Throws what the API server answered (404, 403) for the caller.
 */
export async function readAgentReplicas(namespace: string, name: string): Promise<ReplicaCounts> {
	const deployment = await getAppsApi().readNamespacedDeployment({ namespace, name });
	return { desired: deployment.spec?.replicas ?? 1, ready: deployment.status?.readyReplicas ?? 0 };
}
