import * as k8s from '@kubernetes/client-node';
import { callerToken } from '../auth/identity.js';
import type { BearerToken } from '../auth/bearer-token.js';

let kubeConfig: k8s.KubeConfig | null = null;

// A signed-in caller's configuration is built once per token and forgotten with it.
const callerConfigs = new WeakMap<BearerToken, k8s.KubeConfig>();

function initKubeConfig(): k8s.KubeConfig {
	if (kubeConfig) return kubeConfig;

	kubeConfig = new k8s.KubeConfig();

	// In-cluster config when running in K8s (the client library loads it without error anywhere,
	// so the cluster's own marker decides); the default kubeconfig for local development
	if (process.env.KUBERNETES_SERVICE_HOST) {
		kubeConfig.loadFromCluster();
		console.log('Loaded in-cluster Kubernetes config');
		return kubeConfig;
	}
	try {
		kubeConfig.loadFromDefault();
		console.log('Loaded default Kubernetes config');
	} catch (e) {
		console.error('Failed to load Kubernetes config:', e);
		throw new Error('Unable to load Kubernetes configuration', { cause: e });
	}

	return kubeConfig;
}

/**
 * The dashboard's own ServiceAccount configuration. It is for the few calls that are not made
 * on behalf of a person: checking a pasted token, the health probe, the namespace scope of a
 * workshop instance, and the read views of anonymous read-only mode.
 */
export function serviceAccountKubeConfig(): k8s.KubeConfig {
	return initKubeConfig();
}

/** The same cluster as the dashboard's configuration, with the caller's token as the only credential. */
function configFor(token: BearerToken): k8s.KubeConfig {
	const known = callerConfigs.get(token);
	if (known) return known;
	const cluster = initKubeConfig().getCurrentCluster();
	if (!cluster) throw new Error('No active cluster!');
	const config = new k8s.KubeConfig();
	config.loadFromClusterAndUser(cluster, { name: 'caller', token: token.reveal() });
	callerConfigs.set(token, config);
	return config;
}

/**
 * The Kubernetes configuration of the request being served: the signed-in caller's token, so
 * the caller's own RBAC decides what they see, or the dashboard's ServiceAccount for an
 * anonymous read-only viewer. Every Kubernetes call of a request goes through here.
 */
export function getKubeConfig(): k8s.KubeConfig {
	const token = callerToken();
	return token ? configFor(token) : initKubeConfig();
}

export function getCoreApi(): k8s.CoreV1Api {
	return getKubeConfig().makeApiClient(k8s.CoreV1Api);
}

/** CoreV1 with the dashboard's ServiceAccount, for reading what the instance itself is scoped to. */
export function getServiceAccountCoreApi(): k8s.CoreV1Api {
	return initKubeConfig().makeApiClient(k8s.CoreV1Api);
}

export function getCustomObjectsApi(): k8s.CustomObjectsApi {
	return getKubeConfig().makeApiClient(k8s.CustomObjectsApi);
}

export function getAppsApi(): k8s.AppsV1Api {
	return getKubeConfig().makeApiClient(k8s.AppsV1Api);
}

export function getBatchApi(): k8s.BatchV1Api {
	return getKubeConfig().makeApiClient(k8s.BatchV1Api);
}

export async function checkConnection(): Promise<{ connected: boolean; version?: string; error?: string }> {
	try {
		const versionApi = initKubeConfig().makeApiClient(k8s.VersionApi);
		const versionInfo = await versionApi.getCode();
		return {
			connected: true,
			version: versionInfo.gitVersion
		};
	} catch (e) {
		return {
			connected: false,
			error: e instanceof Error ? e.message : 'Unknown error'
		};
	}
}

export function getApiextensionsApi(): k8s.ApiextensionsV1Api {
	return getKubeConfig().makeApiClient(k8s.ApiextensionsV1Api);
}

export function getCoordinationApi(): k8s.CoordinationV1Api {
	return getKubeConfig().makeApiClient(k8s.CoordinationV1Api);
}
