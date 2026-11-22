// Pure readers for the operator Deployment and pods behind /api/kubemoot/system-info.

import { deploymentImage, deploymentVersion, podSelectorOf } from '#lib/operator-detail.js';

export interface OperatorDeploymentLike {
	metadata?: { labels?: Record<string, string> };
	spec?: {
		selector?: { matchLabels?: Record<string, string> };
		template?: { spec?: { containers?: { image?: string }[] } };
	};
}

export interface OperatorPodLike {
	status?: { phase?: string; startTime?: string | Date };
}

export interface OperatorDeploymentInfo {
	operatorVersion: string;
	operatorImage: string;
	/** Label selector built from the Deployment's matchLabels; empty when it has none. */
	podSelector: string;
}

/** Version, image, and pod selector of the operator Deployment, 'unknown' where absent. */
export function operatorDeploymentInfo(dep: OperatorDeploymentLike): OperatorDeploymentInfo {
	return { operatorVersion: deploymentVersion(dep), operatorImage: deploymentImage(dep), podSelector: podSelectorOf(dep) };
}

/**
 * ISO start time of the oldest Running pod (the leader candidate), or '' when none
 * runs. Pending and terminating pods are ignored so the uptime reflects the version
 * serving traffic.
 */
export function oldestRunningStart(pods: OperatorPodLike[]): string {
	const startTimes = pods
		.map((p) => (p.status?.phase === 'Running' ? p.status.startTime : undefined))
		.filter((t): t is string | Date => !!t)
		.map((t) => new Date(t).getTime())
		.filter((t) => !Number.isNaN(t));
	if (startTimes.length === 0) return '';
	return new Date(Math.min(...startTimes)).toISOString();
}

export interface KubemootConfigLike {
	status?: { release?: unknown };
}

/**
 * The Kubemoot release the operator reports in the KubemootConfig status, or '' when the
 * object is missing, its status is not written yet, or the operator is too old to report one.
 */
export function kubemootRelease(config: KubemootConfigLike | null | undefined): string {
	const release = config?.status?.release;
	return typeof release === 'string' ? release : '';
}
