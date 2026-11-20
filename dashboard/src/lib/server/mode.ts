// Deployment switches read from the environment on every call, so a test can set
// them and a restart is the only way a running dashboard changes mode.
//
// The dashboard is always read-only: the server hook refuses every request that is not
// GET, HEAD or OPTIONS, and no route or page writes to Kubernetes or NATS. Changes are made
// with kmctl, CrewForge or kubectl under the caller's own identity.
//
//   KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR=<label selector>   (e.g. kubemoot.ai/workshop-team=true)
//       Only namespaces matching the selector are shown. Empty means all namespaces.
//   KUBEMOOT_DASHBOARD_INFRA_NAMESPACE=<namespace>            (default: kubemoot)
//       The operator namespace. Infrastructure kinds (model servers, models,
//       embedding models, MCP catalogs and quality policies) stay readable there
//       when a selector is set.

export const NAMESPACE_SELECTOR_ENV = 'KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR';
export const INFRA_NAMESPACE_ENV = 'KUBEMOOT_DASHBOARD_INFRA_NAMESPACE';

/** Methods that cannot change anything; every other method is refused. */
export const SAFE_METHODS: ReadonlySet<string> = new Set(['GET', 'HEAD', 'OPTIONS']);

/** The label selector that limits visible namespaces, or '' for all of them. */
export function namespaceSelector(): string {
	return (process.env[NAMESPACE_SELECTOR_ENV] ?? '').trim();
}

export function isScoped(): boolean {
	return namespaceSelector() !== '';
}

export function infraNamespace(): string {
	return (process.env[INFRA_NAMESPACE_ENV] ?? '').trim() || 'kubemoot';
}

/** The mode the browser needs; sent by the root layout load. */
export interface DashboardMode {
	scoped: boolean;
}

export function dashboardMode(): DashboardMode {
	return { scoped: isScoped() };
}
