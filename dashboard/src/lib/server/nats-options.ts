// The NATS connection options every Kubemoot component reads the same way: the URL
// (tls:// accepted), a CA bundle, an optional client certificate and key (mutual TLS) and one
// kind of credential: a creds file, a user and password, or a token. With none set the
// dashboard connects plainly and anonymously. The names match the Go and Java components.
import { readFileSync } from 'node:fs';
import { credsAuthenticator, type ConnectionOptions } from 'nats';

export const ENV = {
	url: 'NATS_URL',
	caFile: 'NATS_CA_FILE',
	clientCertFile: 'NATS_CLIENT_CERT_FILE',
	clientKeyFile: 'NATS_CLIENT_KEY_FILE',
	credsFile: 'NATS_CREDS_FILE',
	user: 'NATS_USER',
	password: 'NATS_PASSWORD',
	token: 'NATS_TOKEN'
} as const;

type Env = Record<string, string | undefined>;
type ReadFile = (path: string) => Uint8Array;

/** A blank value is absent. */
function get(env: Env, name: string): string {
	return (env[name] ?? '').trim();
}

/** Rejects a combination that cannot work, naming the variables involved. */
export function validateNatsEnv(env: Env): void {
	if (!get(env, ENV.clientCertFile) !== !get(env, ENV.clientKeyFile)) {
		throw new Error(`${ENV.clientCertFile} and ${ENV.clientKeyFile} must be set together`);
	}
	if (!get(env, ENV.user) !== !(env[ENV.password] ?? '')) {
		throw new Error(`${ENV.user} and ${ENV.password} must be set together`);
	}
	const kinds = [get(env, ENV.credsFile), get(env, ENV.user), get(env, ENV.token)].filter(Boolean).length;
	if (kinds > 1) {
		throw new Error(`set only one of ${ENV.credsFile}, ${ENV.user} with ${ENV.password}, or ${ENV.token}`);
	}
}

/** What is configured, never a secret value. */
export function describeNatsEnv(env: Env): string {
	const parts: string[] = [];
	if (get(env, ENV.caFile)) parts.push(`ca ${get(env, ENV.caFile)}`);
	if (get(env, ENV.clientCertFile)) parts.push('client certificate');
	if (get(env, ENV.credsFile)) parts.push('creds file');
	else if (get(env, ENV.user)) parts.push('user and password');
	else if (get(env, ENV.token)) parts.push('token');
	return parts.length ? parts.join(', ') : 'no TLS options, anonymous';
}

function requireFile(variable: string, path: string, read: ReadFile): Uint8Array {
	let bytes: Uint8Array;
	try {
		bytes = read(path);
	} catch (err) {
		throw new Error(`${variable}: cannot read ${path}: ${err instanceof Error ? err.message : String(err)}`, { cause: err });
	}
	if (bytes.length === 0) throw new Error(`${variable}: ${path} is empty`);
	return bytes;
}

function tlsOptions(env: Env, url: string, read: ReadFile): ConnectionOptions['tls'] {
	const caFile = get(env, ENV.caFile);
	const certFile = get(env, ENV.clientCertFile);
	const keyFile = get(env, ENV.clientKeyFile);
	if (!caFile && !certFile && !url.startsWith('tls://')) return undefined;
	const tls: NonNullable<ConnectionOptions['tls']> = {};
	if (caFile) {
		requireFile(ENV.caFile, caFile, read);
		tls.caFile = caFile;
	}
	if (certFile) {
		requireFile(ENV.clientCertFile, certFile, read);
		requireFile(ENV.clientKeyFile, keyFile, read);
		tls.certFile = certFile;
		tls.keyFile = keyFile;
	}
	return tls;
}

/**
 * The TLS and credential part of a connection. Files are read now, so a missing or empty one
 * fails at the first connect naming its variable.
 */
export function natsConnectionOptions(
	env: Env,
	url: string,
	read: ReadFile = readFileSync
): Pick<ConnectionOptions, 'tls' | 'authenticator' | 'user' | 'pass' | 'token'> {
	validateNatsEnv(env);
	const out: Pick<ConnectionOptions, 'tls' | 'authenticator' | 'user' | 'pass' | 'token'> = {};
	const tls = tlsOptions(env, url, read);
	if (tls) out.tls = tls;
	const creds = get(env, ENV.credsFile);
	if (creds) {
		out.authenticator = credsAuthenticator(requireFile(ENV.credsFile, creds, read));
	} else if (get(env, ENV.user)) {
		out.user = get(env, ENV.user);
		out.pass = env[ENV.password] ?? '';
	} else if (get(env, ENV.token)) {
		out.token = get(env, ENV.token);
	}
	return out;
}
