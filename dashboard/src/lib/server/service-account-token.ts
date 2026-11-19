import { readFileSync } from 'node:fs';

/** Where Kubernetes projects the pod's service account token; it is rotated, so it is read on every use. */
export const SERVICE_ACCOUNT_TOKEN_PATH = '/var/run/secrets/kubernetes.io/serviceaccount/token';

/** The pod's service account token, or '' when there is none (local development). */
export function serviceAccountToken(path: string = SERVICE_ACCOUNT_TOKEN_PATH): string {
	try {
		return readFileSync(path, 'utf8').trim();
	} catch {
		return '';
	}
}
