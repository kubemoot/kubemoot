import { describe, expect, it } from 'vitest';
import { describeNatsEnv, natsConnectionOptions, validateNatsEnv } from './nats-options';

const present = () => new TextEncoder().encode('content');
const empty = () => new Uint8Array();
const unreadable = () => {
	throw new Error('ENOENT');
};
const PLAIN = 'nats://nats:4222';

describe('natsConnectionOptions', () => {
	it('sets nothing when nothing is configured', () => {
		expect(natsConnectionOptions({}, PLAIN, present)).toEqual({});
		expect(describeNatsEnv({})).toBe('no TLS options, anonymous');
	});

	it('treats blank values as absent', () => {
		expect(natsConnectionOptions({ NATS_CA_FILE: ' ', NATS_USER: '', NATS_TOKEN: '  ' }, PLAIN, present)).toEqual({});
	});

	it('turns TLS on for a tls:// URL even with no CA', () => {
		expect(natsConnectionOptions({}, 'tls://nats:4222', present).tls).toEqual({});
	});

	it('passes the CA and the client certificate by file', () => {
		const o = natsConnectionOptions(
			{ NATS_CA_FILE: '/ca.crt', NATS_CLIENT_CERT_FILE: '/c.crt', NATS_CLIENT_KEY_FILE: '/c.key' },
			'tls://nats:4222',
			present
		);
		expect(o.tls).toEqual({ caFile: '/ca.crt', certFile: '/c.crt', keyFile: '/c.key' });
	});

	it('passes a user and password, a token, or a creds file', () => {
		expect(natsConnectionOptions({ NATS_USER: 'u', NATS_PASSWORD: 'p' }, PLAIN, present)).toMatchObject({ user: 'u', pass: 'p' });
		expect(natsConnectionOptions({ NATS_TOKEN: 't' }, PLAIN, present)).toMatchObject({ token: 't' });
		expect(natsConnectionOptions({ NATS_CREDS_FILE: '/u.creds' }, PLAIN, present).authenticator).toBeTypeOf('function');
	});

	it.each([
		[{ NATS_CLIENT_CERT_FILE: '/c' }, 'NATS_CLIENT_KEY_FILE'],
		[{ NATS_CLIENT_KEY_FILE: '/k' }, 'NATS_CLIENT_CERT_FILE'],
		[{ NATS_USER: 'u' }, 'NATS_PASSWORD'],
		[{ NATS_PASSWORD: 'p' }, 'NATS_USER'],
		[{ NATS_CREDS_FILE: '/c', NATS_TOKEN: 't' }, 'set only one'],
		[{ NATS_USER: 'u', NATS_PASSWORD: 'p', NATS_TOKEN: 't' }, 'set only one']
	])('refuses a contradiction %j', (env, message) => {
		expect(() => validateNatsEnv(env)).toThrow(message);
	});

	it('names the variable of a file that is missing or empty', () => {
		expect(() => natsConnectionOptions({ NATS_CA_FILE: '/absent' }, PLAIN, unreadable)).toThrow('NATS_CA_FILE: cannot read /absent');
		expect(() => natsConnectionOptions({ NATS_CREDS_FILE: '/u.creds' }, PLAIN, empty)).toThrow('NATS_CREDS_FILE: /u.creds is empty');
	});

	it('never shows a secret in the description', () => {
		const d = describeNatsEnv({ NATS_USER: 'alice', NATS_PASSWORD: 'hunter2' });
		expect(d).not.toContain('alice');
		expect(d).not.toContain('hunter2');
	});
});
