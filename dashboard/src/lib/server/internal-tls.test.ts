// @vitest-environment node
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import https from 'node:https';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { internalFetch, isHttps, isPlainHttp, readCA, resetCA } from './internal-tls';

/** Makes a CA and a localhost serving certificate it signs, with openssl, in a temp directory. */
function makeCerts(dir: string, name: string) {
	const sh = (...args: string[]) => execFileSync('/usr/bin/openssl', args, { cwd: dir, stdio: 'pipe' });
	sh(
		'req',
		'-x509',
		'-newkey',
		'ec',
		'-pkeyopt',
		'ec_paramgen_curve:prime256v1',
		'-nodes',
		'-keyout',
		`${name}-ca.key`,
		'-out',
		`${name}-ca.crt`,
		'-days',
		'2',
		'-subj',
		`/CN=${name}-ca`,
		'-addext',
		'basicConstraints=critical,CA:TRUE',
		'-addext',
		'keyUsage=critical,keyCertSign'
	);
	sh(
		'req',
		'-newkey',
		'ec',
		'-pkeyopt',
		'ec_paramgen_curve:prime256v1',
		'-nodes',
		'-keyout',
		`${name}.key`,
		'-out',
		`${name}.csr`,
		'-subj',
		'/CN=localhost'
	);
	writeFileSync(join(dir, `${name}.ext`), 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n');
	sh(
		'x509',
		'-req',
		'-in',
		`${name}.csr`,
		'-CA',
		`${name}-ca.crt`,
		'-CAkey',
		`${name}-ca.key`,
		'-CAcreateserial',
		'-out',
		`${name}.crt`,
		'-days',
		'2',
		'-extfile',
		`${name}.ext`
	);
}

/** Signs the CSR makeCerts left for `name` with its CA, for a period that ended in the past; returns the file. */
function makeExpiredLeaf(dir: string, name: string): string {
	const sh = (...args: string[]) => execFileSync('/usr/bin/openssl', args, { cwd: dir, stdio: 'pipe' });
	writeFileSync(
		join(dir, 'ca.cnf'),
		'[ca]\ndefault_ca=d\n[d]\ndatabase=index.txt\nnew_certs_dir=.\nserial=serial.txt\ndefault_md=sha256\npolicy=p\nunique_subject=no\n[p]\ncommonName=supplied\n'
	);
	writeFileSync(join(dir, 'index.txt'), '');
	writeFileSync(join(dir, 'serial.txt'), '0A\n');
	const out = `${name}-expired.crt`;
	sh(
		'ca',
		'-batch',
		'-config',
		'ca.cnf',
		'-cert',
		`${name}-ca.crt`,
		'-keyfile',
		`${name}-ca.key`,
		'-in',
		`${name}.csr`,
		'-out',
		out,
		'-startdate',
		'20200101000000Z',
		'-enddate',
		'20200102000000Z',
		'-extfile',
		`${name}.ext`,
		'-notext'
	);
	return join(dir, out);
}

let dir: string;
let server: https.Server;
let base = '';
let seen: string[] = [];

/** An https server whose certificate (signed by the trusted CA) ended in the past. */
async function listenExpired() {
	const seenHere: string[] = [];
	const expiredCrt = makeExpiredLeaf(dir, 'svc');
	const server = https.createServer({ key: readFileSync(join(dir, 'svc.key')), cert: readFileSync(expiredCrt) }, (req, res) => {
		seenHere.push(String(req.headers.authorization));
		res.end('{}');
	});
	await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
	return { server, seen: seenHere, base: `https://localhost:${(server.address() as AddressInfo).port}` };
}

beforeAll(async () => {
	dir = mkdtempSync(join(tmpdir(), 'internal-tls-'));
	makeCerts(dir, 'svc');
	makeCerts(dir, 'other');
	server = https.createServer({ key: readFileSync(join(dir, 'svc.key')), cert: readFileSync(join(dir, 'svc.crt')) }, (req, res) => {
		seen.push(String(req.headers.authorization));
		if (req.url === '/redirect') {
			res.writeHead(302, { Location: '/elsewhere' }).end();
			return;
		}
		if (req.url === '/none') {
			res.writeHead(204).end();
			return;
		}
		res.setHeader('Set-Cookie', ['a=1', 'b=2']).end(`hello ${req.method}`);
	});
	await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
	base = `https://localhost:${(server.address() as AddressInfo).port}`;
});

afterAll(() => {
	server.close();
	rmSync(dir, { recursive: true, force: true });
});

afterEach(() => {
	resetCA();
	seen = [];
});

describe('internalFetch', () => {
	it('connects and sends the token to a server the given CA signed', async () => {
		const res = await internalFetch(`${base}/x`, { headers: { Authorization: 'Bearer t' } }, join(dir, 'svc-ca.crt'));
		expect(res.status).toBe(200);
		expect(await res.text()).toBe('hello GET');
		expect(res.headers.getSetCookie()).toEqual(['a=1', 'b=2']);
		expect(seen).toEqual(['Bearer t']);
	});

	it('refuses a certificate another CA signed, and sends no token', async () => {
		await expect(internalFetch(`${base}/x`, { headers: { Authorization: 'Bearer t' } }, join(dir, 'other-ca.crt'))).rejects.toThrow();
		expect(seen).toEqual([]);
	});

	it('trusts the CA for its own calls only: the same server is refused by the process-wide fetch', async () => {
		await internalFetch(`${base}/x`, {}, join(dir, 'svc-ca.crt'));
		await expect(fetch(`${base}/x`)).rejects.toThrow();
	});

	it('sends a method and a body, and does not follow a redirect', async () => {
		const res = await internalFetch(`${base}/x`, { method: 'POST', body: '{}' }, join(dir, 'svc-ca.crt'));
		expect(await res.text()).toBe('hello POST');
		const redirect = await internalFetch(`${base}/redirect`, {}, join(dir, 'svc-ca.crt'));
		expect(redirect.status).toBe(302);
		expect(seen.length).toBe(2);
	});

	it('answers a body-less status with no body', async () => {
		const res = await internalFetch(`${base}/none`, {}, join(dir, 'svc-ca.crt'));
		expect(res.status).toBe(204);
		expect(await res.text()).toBe('');
	});

	it('is stopped by its signal', async () => {
		const ctl = new AbortController();
		ctl.abort();
		await expect(internalFetch(`${base}/x`, { signal: ctl.signal }, join(dir, 'svc-ca.crt'))).rejects.toThrow();
	});

	it('reads a republished CA bundle without a restart', async () => {
		const file = join(dir, 'published.crt');
		writeFileSync(file, readFileSync(join(dir, 'other-ca.crt')));
		await expect(internalFetch(`${base}/x`, {}, file)).rejects.toThrow();
		writeFileSync(file, readFileSync(join(dir, 'other-ca.crt'), 'utf8') + readFileSync(join(dir, 'svc-ca.crt'), 'utf8'));
		expect((await internalFetch(`${base}/x`, {}, file)).status).toBe(200);
	});

	it('fails, rather than going unverified, when the CA file is missing or holds no certificate', async () => {
		await expect(internalFetch(`${base}/x`, {}, join(dir, 'none.crt'))).rejects.toThrow();
		const empty = join(dir, 'empty.crt');
		writeFileSync(empty, 'nothing');
		await expect(internalFetch(`${base}/x`, {}, empty)).rejects.toThrow(/no certificate/);
		expect(() => readCA(empty)).toThrow();
	});
});

describe('the scheme rule', () => {
	it('ignores the case of the scheme', () => {
		// The plain-http strings are the input under test.
		/* eslint-disable sonarjs/no-clear-text-protocols */
		expect(isHttps('HTTPS://x')).toBe(true);
		expect(isHttps('http://x')).toBe(false);
		expect(isPlainHttp('HTTP://x')).toBe(true);
		/* eslint-enable sonarjs/no-clear-text-protocols */
		expect(isPlainHttp('https://x')).toBe(false);
	});

	it('uses the ordinary fetch when no CA file is configured', async () => {
		const fetchMock = vi.fn(async () => new Response('{}'));
		vi.stubGlobal('fetch', fetchMock);
		await internalFetch('https://report.test/x', { headers: { Authorization: 'Bearer t' } }, '');
		expect(fetchMock).toHaveBeenCalledWith('https://report.test/x', { headers: { Authorization: 'Bearer t' } });
		vi.unstubAllGlobals();
	});
});

describe('an expired server certificate', () => {
	it('is refused and no token is sent', async () => {
		const expired = await listenExpired();
		try {
			await expect(
				internalFetch(`${expired.base}/x`, { headers: { Authorization: 'Bearer t' } }, join(dir, 'svc-ca.crt'))
			).rejects.toThrow();
			expect(expired.seen).toEqual([]);
		} finally {
			expired.server.close();
		}
	});
});
