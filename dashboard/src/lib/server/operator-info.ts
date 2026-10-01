// Pure readers for the operator Deployment and pods behind /api/kubemoot/system-info.

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

function firstContainerImage(dep: OperatorDeploymentLike): string | undefined {
	return dep.spec?.template?.spec?.containers?.[0]?.image;
}

function podSelectorOf(dep: OperatorDeploymentLike): string {
	return Object.entries(dep.spec?.selector?.matchLabels ?? {})
		.map(([k, v]) => `${k}=${v}`)
		.join(',');
}

/** Version, image, and pod selector of the operator Deployment, 'unknown' where absent. */
export function operatorDeploymentInfo(dep: OperatorDeploymentLike): OperatorDeploymentInfo {
	return {
		operatorVersion: dep.metadata?.labels?.['app.kubernetes.io/version'] || 'unknown',
		operatorImage: firstContainerImage(dep) ?? 'unknown',
		podSelector: podSelectorOf(dep)
	};
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
