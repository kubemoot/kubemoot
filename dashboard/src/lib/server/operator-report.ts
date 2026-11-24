import { OPERATOR_REPORT_URL } from '$app/env/private';
import { callerToken } from '#lib/server/auth/identity.js';
import { isPlainHttp } from '#lib/server/internal-tls.js';
import { serviceAccountToken } from '#lib/server/service-account-token.js';

// The operator's report service (component health, fitness XLSX) inside the cluster. It serves
// TLS with a certificate the install CA signed, and every call carries the signed-in person's
// token, so the dashboard verifies it against that CA (internalFetch). The dashboard reaches it
// from its own pod and browsers never see this URL. OPERATOR_REPORT_URL overrides it.
export const IN_CLUSTER_REPORT_URL = 'https://kubemoot-operator-report.kubemoot:8082';

/** Base URL of the operator report service. */
export function operatorReportBase(): string {
	const url = OPERATOR_REPORT_URL || IN_CLUSTER_REPORT_URL;
	if (isPlainHttp(url)) warnPlainOnce(url);
	return url;
}

let warned = false;

/** Says once that the report calls go over plain http and carry the person's token. */
function warnPlainOnce(url: string): void {
	if (warned) return;
	warned = true;
	console.warn(`OPERATOR_REPORT_URL ${url} is plain http: the signed-in person's bearer token crosses the network in the clear`);
}

/**
 * The bearer every report service endpoint checks (a TokenReview and a SubjectAccessReview):
 * the signed-in caller's own token, so the caller's RBAC decides what a report shows. An
 * anonymous read-only viewer has none of their own and gets the dashboard's service account
 * token; with no token at all the operator answers 401 and says what to send.
 * Every fetch to the report service sends these headers.
 */
export function operatorAuthHeaders(): Record<string, string> {
	const token = callerToken()?.reveal() ?? serviceAccountToken();
	return token ? { Authorization: `Bearer ${token}` } : {};
}
