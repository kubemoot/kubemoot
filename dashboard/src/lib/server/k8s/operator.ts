import { getApiextensionsApi, getAppsApi, getCoordinationApi, getCoreApi } from './client.js';
import { failedReading, readingOf } from './errors.js';
import { infraNamespace } from '../mode.js';
import { oldestRunningStart, type OperatorPodLike } from '../operator-info.js';
import { kubemootCrds, leaderPod, operatorDeployment, podSelectorOf, type OperatorCrd, type OperatorDeployment, type OperatorDetail } from '#lib/operator-detail.js';
import type { Reading } from '#lib/reading.js';

/** The Deployment the chart makes for the operator. */
export const OPERATOR_DEPLOYMENT = 'kubemoot-operator';

interface DeploymentRead {
	deployment: Reading<OperatorDeployment>;
	/** The label selector of the operator's pods, from the Deployment; empty when it could not be read. */
	selector: string;
}

async function readDeployment(namespace: string): Promise<DeploymentRead> {
	try {
		const dep = await getAppsApi().readNamespacedDeployment({ name: OPERATOR_DEPLOYMENT, namespace });
		return { deployment: { available: true, data: operatorDeployment(dep) }, selector: podSelectorOf(dep) };
	} catch (e) {
		const missing = `the operator Deployment ${OPERATOR_DEPLOYMENT} is not in the ${namespace} namespace`;
		return { deployment: failedReading(e, 'the operator Deployment', missing), selector: '' };
	}
}

async function readStartedAt(namespace: string, selector: string): Promise<Reading<string>> {
	if (!selector) return { available: false, reason: 'the operator pods could not be found without the Deployment' };
	return readingOf('the operator pods', async () => {
		const pods = await getCoreApi().listNamespacedPod({ namespace, labelSelector: selector });
		return oldestRunningStart((pods as { items?: OperatorPodLike[] }).items ?? []);
	});
}

function readLeader(namespace: string): Promise<Reading<string>> {
	return readingOf('the operator leader lease', async () => {
		const leases = await getCoordinationApi().listNamespacedLease({ namespace });
		return leaderPod(leases.items, OPERATOR_DEPLOYMENT);
	});
}

function readCrds(): Promise<Reading<OperatorCrd[]>> {
	return readingOf('the custom resource definitions', async () => kubemootCrds((await getApiextensionsApi().listCustomResourceDefinition()).items));
}

/**
 * The operator as the signed-in person can read it: the Deployment (version, conditions, replicas),
 * the pods (uptime), the leader lease, and the Kubemoot CRDs. Every read is made with the caller's
 * token; each says why it is missing when the caller's RBAC refuses it.
 */
export async function readOperatorDetail(): Promise<OperatorDetail> {
	const namespace = infraNamespace();
	const [{ deployment, selector }, leader, crds] = await Promise.all([readDeployment(namespace), readLeader(namespace), readCrds()]);
	return { deployment, startedAt: await readStartedAt(namespace, selector), leader, crds };
}
