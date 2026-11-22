// How the top bar names the signed-in person: a service account by its own name, anyone else by
// the username the cluster gave. The full identity goes in the tooltip.

const SERVICE_ACCOUNT = /^system:serviceaccount:[^:]+:([^:]+)$/;

/** The short name to show: a service account's name, else the username as it is. */
export function shortIdentity(username: string): string {
	return SERVICE_ACCOUNT.exec(username)?.[1] ?? username;
}

/** The tooltip: the full identity, and what being signed in means. */
export function identityTitle(username: string): string {
	return `${username}: you are signed in with your own Kubernetes token`;
}
