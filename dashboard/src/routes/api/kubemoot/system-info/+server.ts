import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import * as k8s from '@kubernetes/client-node';

/**
 * GET /api/kubemoot/system-info
 *
 * Returns version metadata for kubemoot components the user can't otherwise
 * see in the dashboard: the operator's image version (from the Deployment's
 * app.kubernetes.io/version label) plus the running operator pod's start time
 * (so the popover can render "Uptime: 3h 42m" — a quick way to confirm the
 * operator actually rolled to a new version after a CI/CD release).
 *
 * Deliberately narrow scope to avoid duplicating what's already on /config
 * (KubemootConfig.spec.images — agent runtime, MCP gateway, indexer, etc.)
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
	 * OLDEST ready pod's start time — i.e., the one that's been serving
	 * longest and likely holds the lease.
	 */
	operatorStartedAt: string;
}

const OPERATOR_NAMESPACE = 'kubemoot';
const OPERATOR_DEPLOYMENT = 'kubemoot-operator';

export const GET: RequestHandler = async () => {
	const kc = new k8s.KubeConfig();
	try {
		kc.loadFromCluster();
	} catch {
		kc.loadFromDefault();
	}

	const apps = kc.makeApiClient(k8s.AppsV1Api);
	const core = kc.makeApiClient(k8s.CoreV1Api);

	let operatorVersion = 'unknown';
	let operatorImage = 'unknown';
	let podSelector = '';
	try {
		const dep = await apps.readNamespacedDeployment({
			name: OPERATOR_DEPLOYMENT,
			namespace: OPERATOR_NAMESPACE
		});
		const labels = (dep as { metadata?: { labels?: Record<string, string> } }).metadata?.labels ?? {};
		operatorVersion = labels['app.kubernetes.io/version'] || 'unknown';
		operatorImage =
			(dep as { spec?: { template?: { spec?: { containers?: { image?: string }[] } } } })
				.spec?.template?.spec?.containers?.[0]?.image ?? 'unknown';
		// Build label selector from the deployment's matchLabels so the pod
		// list is exact regardless of which key the chart used (we've seen
		// both control-plane=controller-manager and app=kubemoot-operator).
		const match =
			(dep as { spec?: { selector?: { matchLabels?: Record<string, string> } } }).spec?.selector
				?.matchLabels ?? {};
		podSelector = Object.entries(match)
			.map(([k, v]) => `${k}=${v}`)
			.join(',');
	} catch (err) {
		console.error('Failed to read kubemoot-operator deployment:', err);
	}

	let operatorStartedAt = '';
	if (podSelector) {
		try {
			const podList = await core.listNamespacedPod({
				namespace: OPERATOR_NAMESPACE,
				labelSelector: podSelector
			});
			const items =
				(podList as { items?: { status?: { phase?: string; startTime?: string | Date } }[] })
					.items ?? [];
			// Pick the OLDEST Running pod — the leader candidate. Ignore
			// Pending/Terminating pods during a rollout so the displayed
			// uptime always reflects the version currently serving traffic.
			const startTimes = items
				.filter((p) => p.status?.phase === 'Running' && p.status?.startTime)
				.map((p) => new Date(p.status!.startTime as string | Date).getTime())
				.filter((t) => !Number.isNaN(t));
			if (startTimes.length > 0) {
				operatorStartedAt = new Date(Math.min(...startTimes)).toISOString();
			}
		} catch (err) {
			console.error('Failed to read kubemoot-operator pods:', err);
		}
	}

	const info: SystemInfo = { operatorVersion, operatorImage, operatorStartedAt };
	return json(info);
};
