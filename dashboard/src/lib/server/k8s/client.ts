import * as k8s from '@kubernetes/client-node';

let kubeConfig: k8s.KubeConfig | null = null;
let coreApi: k8s.CoreV1Api | null = null;
let customObjectsApi: k8s.CustomObjectsApi | null = null;
let batchApi: k8s.BatchV1Api | null = null;

function initKubeConfig(): k8s.KubeConfig {
	if (kubeConfig) return kubeConfig;

	kubeConfig = new k8s.KubeConfig();

	// Try in-cluster config first (when running in K8s)
	try {
		kubeConfig.loadFromCluster();
		console.log('Loaded in-cluster Kubernetes config');
	} catch {
		// Fall back to default config (local development)
		try {
			kubeConfig.loadFromDefault();
			console.log('Loaded default Kubernetes config');
		} catch (e) {
			console.error('Failed to load Kubernetes config:', e);
			throw new Error('Unable to load Kubernetes configuration', { cause: e });
		}
	}

	return kubeConfig;
}

export function getCoreApi(): k8s.CoreV1Api {
	if (coreApi) return coreApi;

	const config = initKubeConfig();
	coreApi = config.makeApiClient(k8s.CoreV1Api);
	return coreApi;
}

export function getCustomObjectsApi(): k8s.CustomObjectsApi {
	if (customObjectsApi) return customObjectsApi;

	const config = initKubeConfig();
	customObjectsApi = config.makeApiClient(k8s.CustomObjectsApi);
	return customObjectsApi;
}

export function getBatchApi(): k8s.BatchV1Api {
	if (batchApi) return batchApi;

	const config = initKubeConfig();
	batchApi = config.makeApiClient(k8s.BatchV1Api);
	return batchApi;
}

export function getKubeConfig(): k8s.KubeConfig {
	return initKubeConfig();
}

export async function checkConnection(): Promise<{ connected: boolean; version?: string; error?: string }> {
	try {
		const config = initKubeConfig();
		const versionApi = config.makeApiClient(k8s.VersionApi);
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
