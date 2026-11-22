// Deployment switches read from the environment on every call, so a test can set
// them and a restart is the only way a running dashboard changes mode.
//
// The dashboard never writes to Kubernetes or NATS: the server hook refuses every request that
// is not GET, HEAD or OPTIONS, except sign-in, sign-out and asking a crew, which a signed-in
// person does through the crew's discussion gateway with their own token. Other changes are
// made with kmctl, CrewForge or kubectl under the caller's own identity.
//
//   KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR=<label selector>   (e.g. kubemoot.ai/workshop-team=true)
//       Only namespaces matching the selector are shown. Empty means all namespaces.
//   KUBEMOOT_DASHBOARD_ANONYMOUS_READ_ONLY=true               (default: false)
//       Without it every page needs a sign-in. With it, a visitor who has not signed in sees
//       the read views with the dashboard's own ServiceAccount and can never ask or change
//       anything.
//   KUBEMOOT_DASHBOARD_SESSION_MAX_MINUTES=<minutes>           (default: 480)
//       The longest a sign-in lasts; a token that expires sooner ends it sooner.
//   KUBEMOOT_DASHBOARD_GATEWAY_URL=<url template>               (default: in-cluster Service)
//       Where a crew's discussion gateway is; {crew} and {namespace} are replaced. Empty uses
//       http://<crew>-discussion.<namespace>.svc.cluster.local.
//   KUBEMOOT_DASHBOARD_INFRA_NAMESPACE=<namespace>            (default: kubemoot)
//       The operator namespace. Infrastructure kinds (model servers, models,
//       embedding models, MCP catalogs and quality policies) stay readable there
//       when a selector is set.

export const NAMESPACE_SELECTOR_ENV = 'KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR';
export const INFRA_NAMESPACE_ENV = 'KUBEMOOT_DASHBOARD_INFRA_NAMESPACE';
export const ANONYMOUS_READ_ONLY_ENV = 'KUBEMOOT_DASHBOARD_ANONYMOUS_READ_ONLY';
export const SESSION_MAX_MINUTES_ENV = 'KUBEMOOT_DASHBOARD_SESSION_MAX_MINUTES';
export const GATEWAY_URL_ENV = 'KUBEMOOT_DASHBOARD_GATEWAY_URL';

const DEFAULT_SESSION_MINUTES = 480;

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
	/** Visitors who have not signed in may see the read views. */
	anonymousReadOnly: boolean;
}

export function dashboardMode(): DashboardMode {
	return { scoped: isScoped(), anonymousReadOnly: anonymousReadOnly() };
}

/** True when a visitor who has not signed in may see the read views (never ask or change). */
export function anonymousReadOnly(): boolean {
	return (process.env[ANONYMOUS_READ_ONLY_ENV] ?? '').trim().toLowerCase() === 'true';
}

/** How long a sign-in lasts at most, in milliseconds. */
export function sessionMaxMs(): number {
	const minutes = Number((process.env[SESSION_MAX_MINUTES_ENV] ?? '').trim());
	return (Number.isFinite(minutes) && minutes > 0 ? minutes : DEFAULT_SESSION_MINUTES) * 60_000;
}

/** The gateway URL template from the environment, or '' for the in-cluster Service. */
export function gatewayUrlTemplate(): string {
	return (process.env[GATEWAY_URL_ENV] ?? '').trim();
}
