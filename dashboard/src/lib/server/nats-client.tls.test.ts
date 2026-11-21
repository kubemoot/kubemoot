// Against a real nats-server that demands TLS with a client certificate and a user. Runs only when
// NATS_SERVER_BIN points at a binary and openssl is installed.
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { connectTo } from './nats-client';

const OPENSSL = '/usr/bin/openssl';
const bin = process.env.NATS_SERVER_BIN ?? '';
const canRun = bin !== '' && existsSync(bin) && existsSync(OPENSSL) && hasOpenssl();

function hasOpenssl(): boolean {
	try {
		execFileSync(OPENSSL, ['version'], { stdio: 'ignore' });
		return true;
	} catch {
		return false;
	}
}

function freePort(): Promise<number> {
	return new Promise((resolve, reject) => {
		const s = createServer();
		s.listen(0, '127.0.0.1', () => {
			const port = (s.address() as { port: number }).port;
			s.close(() => resolve(port));
		});
		s.on('error', reject);
	});
}

describe.skipIf(!canRun)('the dashboard connects to a TLS NATS', () => {
	let dir = '';
	let server: ChildProcess | undefined;
	let url = '';
	const sh = (...args: string[]) => execFileSync(OPENSSL, args, { cwd: dir, stdio: 'ignore' });
	const file = (name: string) => join(dir, name);
	const full = () => ({
		NATS_CA_FILE: file('ca.crt'),
		NATS_CLIENT_CERT_FILE: file('client.crt'),
		NATS_CLIENT_KEY_FILE: file('client.key'),
		NATS_USER: 'kubemoot',
		NATS_PASSWORD: 's3cret-pass'
	});

	beforeAll(async () => {
		dir = mkdtempSync(join(tmpdir(), 'nats-tls-'));
		sh('genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256', '-out', 'ca.key');
		sh('req', '-x509', '-new', '-key', 'ca.key', '-subj', '/CN=test CA', '-days', '2', '-out', 'ca.crt');
		for (const leaf of ['server', 'client']) {
			sh('genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256', '-out', `${leaf}.key`);
			sh('req', '-new', '-key', `${leaf}.key`, '-subj', `/CN=${leaf}`, '-out', `${leaf}.csr`);
			writeFileSync(file(`${leaf}.ext`), 'subjectAltName=IP:127.0.0.1,DNS:localhost\n');
			sh('x509', '-req', '-in', `${leaf}.csr`, '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '2', '-extfile', `${leaf}.ext`, '-out', `${leaf}.crt`);
		}
		const port = await freePort();
		writeFileSync(
			file('nats.conf'),
			`host: 127.0.0.1\nport: ${port}\nauthorization { user: kubemoot, password: "s3cret-pass" }\n` +
				`tls { cert_file: "${file('server.crt')}", key_file: "${file('server.key')}", ca_file: "${file('ca.crt')}", verify: true, timeout: 5 }\n`
		);
		server = spawn(bin, ['-c', file('nats.conf')], { stdio: 'ignore' });
		url = `tls://127.0.0.1:${port}`;
		for (let i = 0; i < 50; i++) {
			const up = await new Promise<boolean>((resolve) => {
				const s = createServer();
				s.once('error', () => resolve(true)); // the port is taken: the server is listening
				s.listen(port, '127.0.0.1', () => s.close(() => resolve(false)));
			});
			if (up) return;
			await new Promise((r) => setTimeout(r, 100));
		}
		throw new Error('nats-server did not start');
	});

	afterAll(() => {
		server?.kill();
		if (dir) rmSync(dir, { recursive: true, force: true });
	});

	it('connects with the CA, the client certificate and the credential', async () => {
		const nc = await connectTo(url, full(), { maxReconnectAttempts: 0 });
		expect(nc.isClosed()).toBe(false);
		await nc.close();
	});

	it.each(['NATS_CA_FILE', 'NATS_CLIENT_CERT_FILE', 'NATS_USER'])('is refused without %s, naming the server', async (drop) => {
		const env: Record<string, string | undefined> = { ...full() };
		delete env[drop];
		if (drop === 'NATS_CLIENT_CERT_FILE') delete env.NATS_CLIENT_KEY_FILE;
		if (drop === 'NATS_USER') delete env.NATS_PASSWORD;
		await expect(connectTo(url, env, { maxReconnectAttempts: 0, timeout: 2000 })).rejects.toThrow(`connecting to NATS at ${url}`);
	});
});
