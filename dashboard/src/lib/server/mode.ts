// Deployment switches read from the environment on every call, so a test can set
// them and a restart is the only way a running dashboard changes mode.
//
// The dashboard never writes to NATS, and writes to Kubernetes only to delete a Crew or a
// CrewFitnessSuite as the signed-in person: the server hook refuses every request that is not
// GET, HEAD or OPTIONS, except sign-in, sign-out, the asking and conversation actions a signed-in
// person does through the crew's discussion gateway with their own token, and those two deletes
// with their own token. Other changes are made with kmctl, CrewForge or kubectl under the
// caller's own identity.
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
//       https://<crew>-discussion.<namespace>.svc.cluster.local, a gateway that serves TLS from the
//       install CA (KUBEMOOT_DASHBOARD_CA_FILE below).
//   KUBEMOOT_DASHBOARD_CA_FILE=<path>                           (default: empty)
//       The install CA's public certificates (the chart mounts them from the ConfigMap the
//       operator publishes). An https call that carries a person's token, to the operator's report
//       service, is verified against them and against nothing else; a certificate they did not
//       sign is refused. Only those calls use them: the rest of the process trusts what it did.
//   KUBEMOOT_DASHBOARD_CREW_NAMESPACE_DISCOVERY=true          (default: false)
//       The ServiceAccount may list Crews, so namespaces holding crews are also found that way.
//   KUBEMOOT_DASHBOARD_PROMETHEUS_URL=<url>                    (default: empty)
//       A Prometheus the dashboard reads node accelerator busy and memory from (chart value
//       metrics.prometheusUrl). Empty shows no busy or memory bars.
//   KUBEMOOT_DASHBOARD_TRUSTED_PROXY=true                      (default: false)
//       Accept "Authorization: Bearer <ID token>" from the sign-in proxy (oauth2-proxy) and only
//       from it: the sources are the addresses behind KUBEMOOT_DASHBOARD_TRUSTED_PROXY_HOST (a
//       DNS name, usually a headless Service) and the comma-separated
//       KUBEMOOT_DASHBOARD_TRUSTED_PROXY_ADDRESSES. Off, the header is ignored. On with no
//       address to trust, nothing is trusted.
//   KUBEMOOT_DASHBOARD_INFRA_NAMESPACE=<namespace>            (default: kubemoot)
//       The operator namespace. Infrastructure kinds (model servers, models,
//       embedding models, MCP catalogs and quality policies) stay readable there
//       when a selector is set.

export const NAMESPACE_SELECTOR_ENV = 'KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR';
export const INFRA_NAMESPACE_ENV = 'KUBEMOOT_DASHBOARD_INFRA_NAMESPACE';
export const ANONYMOUS_READ_ONLY_ENV = 'KUBEMOOT_DASHBOARD_ANONYMOUS_READ_ONLY';
export const CREW_NAMESPACE_DISCOVERY_ENV = 'KUBEMOOT_DASHBOARD_CREW_NAMESPACE_DISCOVERY';
export const SESSION_MAX_MINUTES_ENV = 'KUBEMOOT_DASHBOARD_SESSION_MAX_MINUTES';
export const GATEWAY_URL_ENV = 'KUBEMOOT_DASHBOARD_GATEWAY_URL';
export const CA_FILE_ENV = 'KUBEMOOT_DASHBOARD_CA_FILE';
export const PROMETHEUS_URL_ENV = 'KUBEMOOT_DASHBOARD_PROMETHEUS_URL';

export const TRUSTED_PROXY_ENV = 'KUBEMOOT_DASHBOARD_TRUSTED_PROXY';
export const TRUSTED_PROXY_HOST_ENV = 'KUBEMOOT_DASHBOARD_TRUSTED_PROXY_HOST';
export const TRUSTED_PROXY_ADDRESSES_ENV = 'KUBEMOOT_DASHBOARD_TRUSTED_PROXY_ADDRESSES';

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

/** True when the ServiceAccount may list Crews to find namespaces (chart value rbac.crewNamespaceDiscovery). */
export function crewNamespaceDiscovery(): boolean {
	return (process.env[CREW_NAMESPACE_DISCOVERY_ENV] ?? '').trim().toLowerCase() === 'true';
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

/** The file holding the install CA's public certificates, or '' when none is set. */
export function caFile(): string {
	return (process.env[CA_FILE_ENV] ?? '').trim();
}

/** The Prometheus address accelerator use is read from, or '' when none is set. */
export function prometheusUrl(): string {
	return (process.env[PROMETHEUS_URL_ENV] ?? '').trim();
}

/** True when a bearer token from the sign-in proxy may open a session (chart value auth.trustedProxy.enabled). */
export function trustedProxyEnabled(): boolean {
	return (process.env[TRUSTED_PROXY_ENV] ?? '').trim().toLowerCase() === 'true';
}

/** The DNS name whose addresses are the sign-in proxy's, or '' when none is set. */
export function trustedProxyHost(): string {
	return (process.env[TRUSTED_PROXY_HOST_ENV] ?? '').trim();
}

/** The proxy addresses named directly in the environment. */
export function trustedProxyAddresses(): string[] {
	return (process.env[TRUSTED_PROXY_ADDRESSES_ENV] ?? '')
		.split(',')
		.map((a) => a.trim())
		.filter((a) => a !== '');
}
