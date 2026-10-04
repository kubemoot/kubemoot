import type { RequestHandler } from './$types';
import * as k8s from '@kubernetes/client-node';
import { infraNamespace } from '#lib/server/mode.js';
import {
	oldestRunningStart,
	operatorDeploymentInfo,
	type OperatorDeploymentInfo,
	type OperatorPodLike
} from '#lib/server/operator-info.js';

/**
 * GET /api/kubemoot/system-info
 *
 * Returns version metadata for kubemoot components the user can't otherwise
 * see in the dashboard: the operator's image version (from the Deployment's
 * app.kubernetes.io/version label) plus the running operator pod's start time
 * (so the popover can render "Uptime: 3h 42m" - a quick way to confirm the
 * operator actually rolled to a new version after a CI/CD release).
 *
 * Deliberately narrow scope to avoid duplicating what's already on /config
 * (KubemootConfig.spec.images - agent runtime, MCP gateway, indexer, etc.)
 * and /nodes (cluster info). The popover that consumes this endpoint links
 * users to those existing pages for the deeper info.
 */

interface SystemInfo {
	operatorVersion: string;
	operatorImage: string;
	/**
	 * ISO-8601 timestamp the currently-running operator pod started at.
	 * Empty string when no pod is ready (e.g., during a rollout). When the
	 * deployment runs multiple replicas (leader-election HA), this is the
	 * OLDEST ready pod's start time - i.e., the one that's been serving
	 * longest and likely holds the lease.
	 */
	operatorStartedAt: string;
}

const OPERATOR_DEPLOYMENT = 'kubemoot-operator';

function kubeConfig(): k8s.KubeConfig {
	const kc = new k8s.KubeConfig();
	try {
		kc.loadFromCluster();
	} catch {
		kc.loadFromDefault();
	}
	return kc;
}

async function readDeploymentInfo(apps: k8s.AppsV1Api): Promise<OperatorDeploymentInfo> {
	try {
		const dep = await apps.readNamespacedDeployment({
			name: OPERATOR_DEPLOYMENT,
			namespace: infraNamespace()
		});
		// The selector comes from the Deployment's matchLabels so the pod list is exact
		// whichever key the chart used (control-plane=controller-manager or app=kubemoot-operator).
		return operatorDeploymentInfo(dep);
	} catch (err) {
		console.error('Failed to read kubemoot-operator deployment:', err);
		return { operatorVersion: 'unknown', operatorImage: 'unknown', podSelector: '' };
	}
}

async function readStartedAt(core: k8s.CoreV1Api, podSelector: string): Promise<string> {
	if (!podSelector) return '';
	try {
		const podList = await core.listNamespacedPod({
			namespace: infraNamespace(),
			labelSelector: podSelector
		});
		return oldestRunningStart((podList as { items?: OperatorPodLike[] }).items ?? []);
	} catch (err) {
		console.error('Failed to read kubemoot-operator pods:', err);
		return '';
	}
}

export const GET: RequestHandler = async () => {
	const kc = kubeConfig();
	const { operatorVersion, operatorImage, podSelector } = await readDeploymentInfo(
		kc.makeApiClient(k8s.AppsV1Api)
	);
	const operatorStartedAt = await readStartedAt(kc.makeApiClient(k8s.CoreV1Api), podSelector);

	const info: SystemInfo = { operatorVersion, operatorImage, operatorStartedAt };
	return Response.json(info);
};
