// Deployment switches read from the environment on every call, so a test can set
// them and a restart is the only way a running dashboard changes mode.
//
//   KUBEMOOT_DASHBOARD_READ_ONLY=true
//       Every request that is not GET, HEAD or OPTIONS is refused with 403 by the
//       server hook, and the UI hides its write controls.
//   KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR=<label selector>   (e.g. kubemoot.ai/workshop-team=true)
//       Only namespaces matching the selector are shown. Empty means all namespaces.
//   KUBEMOOT_DASHBOARD_INFRA_NAMESPACE=<namespace>            (default: kubemoot)
//       The operator namespace. Infrastructure kinds (model providers, models,
//       embedding models, MCP catalogs and quality policies) stay readable there
//       when a selector is set.

const TRUE_VALUES = new Set(['1', 'true', 'yes', 'on']);

export const READ_ONLY_ENV = 'KUBEMOOT_DASHBOARD_READ_ONLY';
export const NAMESPACE_SELECTOR_ENV = 'KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR';
export const INFRA_NAMESPACE_ENV = 'KUBEMOOT_DASHBOARD_INFRA_NAMESPACE';

/** Methods that cannot change anything; every other method is refused in read-only mode. */
export const SAFE_METHODS: ReadonlySet<string> = new Set(['GET', 'HEAD', 'OPTIONS']);

export function isReadOnly(): boolean {
	return TRUE_VALUES.has((process.env[READ_ONLY_ENV] ?? '').trim().toLowerCase());
}

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

/** The mode the browser needs to hide controls; sent by the root layout load. */
export interface DashboardMode {
	readOnly: boolean;
	scoped: boolean;
}

export function dashboardMode(): DashboardMode {
	return { readOnly: isReadOnly(), scoped: isScoped() };
}
