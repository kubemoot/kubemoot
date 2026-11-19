import { OPERATOR_REPORT_URL } from '$app/env/private';
import { serviceAccountToken } from '#lib/server/service-account-token.js';

// The operator's report service (component health, fitness XLSX) inside the cluster.
// It serves plain HTTP on the pod network only; the dashboard reaches it from its own
// pod and browsers never see this URL. OPERATOR_REPORT_URL overrides it.
// eslint-disable-next-line sonarjs/no-clear-text-protocols -- in-cluster Service, no TLS listener; reviewed hotspot
export const IN_CLUSTER_REPORT_URL = 'http://kubemoot-operator-report.kubemoot:8082';

/** Base URL of the operator report service. */
export function operatorReportBase(): string {
	return OPERATOR_REPORT_URL || IN_CLUSTER_REPORT_URL;
}

/**
 * The dashboard's service account token as the bearer every report service endpoint checks;
 * none when it has no token, which the operator answers with a 401 that says what to send.
 * Every fetch to the report service sends these headers.
 */
export function operatorAuthHeaders(): Record<string, string> {
	const token = serviceAccountToken();
	return token ? { Authorization: `Bearer ${token}` } : {};
}
