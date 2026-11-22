import * as k8s from '@kubernetes/client-node';
import { serviceAccountKubeConfig } from '../k8s/client.js';

/** Why a sign-in was refused, in the terms a person can act on. */
export type SignInFailure = 'empty' | 'malformed' | 'invalid' | 'expired' | 'unavailable';

export class SignInError extends Error {
	constructor(
		readonly failure: SignInFailure,
		message: string
	) {
		super(message);
		this.name = 'SignInError';
	}
}

/** What the API server says about a token it accepted. */
export interface ReviewedToken {
	username: string;
}

/** Checks a token with the API server; the sign-in uses this, tests substitute their own. */
export type TokenReviewer = (token: string) => Promise<ReviewedToken>;

/**
 * Asks the API server who a token belongs to (a TokenReview, made with the dashboard's own
 * ServiceAccount, which needs only the right to create TokenReviews). The token is never
 * trusted unchecked. A token the API server does not accept, and a review that cannot be
 * made, are different failures: the first is the person's to fix, the second is not.
 */
export const reviewToken: TokenReviewer = async (token) => {
	let status: k8s.V1TokenReviewStatus | undefined;
	try {
		const api = serviceAccountKubeConfig().makeApiClient(k8s.AuthenticationV1Api);
		status = (await api.createTokenReview({ body: { spec: { token } } })).status;
	} catch {
		throw new SignInError('unavailable', 'The cluster could not check the token. Try again, or ask the administrator.');
	}
	const username = status?.user?.username;
	if (!status?.authenticated || !username) {
		throw new SignInError('invalid', 'The cluster does not accept this token. It may be expired or mistyped.');
	}
	return { username };
};
