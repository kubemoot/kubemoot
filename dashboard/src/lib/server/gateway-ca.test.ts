// @vitest-environment node
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import https from 'node:https';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { BearerToken } from './auth/bearer-token';
import { runWithIdentity } from './auth/identity';

const mocks = vi.hoisted(() => ({ getCrew: vi.fn() }));
vi.mock('./k8s/kubemoot-crds.js', () => ({ getCrew: mocks.getCrew }));
vi.mock('./scope.js', () => ({ guardNamespace: async () => null }));

import { callGateway } from './gateway';
import { resetCA } from './internal-tls';
import { CA_FILE_ENV, GATEWAY_URL_ENV } from './mode';
import { makeCerts } from './testing/tls-certs';

const asCaller = <T>(fn: () => T) =>
	runWithIdentity({ kind: 'caller', username: 'u', token: new BearerToken('callers-token'), ended: new AbortController().signal }, fn);

let dir: string;
let server: https.Server;
let seen: string[] = [];
let base = '';

beforeAll(async () => {
	dir = mkdtempSync(join(tmpdir(), 'gateway-ca-'));
	makeCerts(dir, 'gw');
	makeCerts(dir, 'other');
	server = https.createServer({ key: readFileSync(join(dir, 'gw.key')), cert: readFileSync(join(dir, 'gw.crt')) }, (req, res) => {
		seen.push(String(req.headers.authorization));
		res.setHeader('Connection', 'close').end('{}');
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
	delete process.env[CA_FILE_ENV];
	delete process.env[GATEWAY_URL_ENV];
	seen = [];
});

describe('the dashboard calling a gateway over https', () => {
	it('refuses a certificate it has no CA for, and sends no token', async () => {
		process.env[GATEWAY_URL_ENV] = base;
		await expect(asCaller(() => callGateway('ns', 'pilot', '/x'))).rejects.toThrow();
		expect(seen).toEqual([]);
	});

	it('refuses a certificate signed by a different CA than the one it is given', async () => {
		process.env[GATEWAY_URL_ENV] = base;
		process.env[CA_FILE_ENV] = join(dir, 'other-ca.crt');
		await expect(asCaller(() => callGateway('ns', 'pilot', '/x'))).rejects.toThrow();
		expect(seen).toEqual([]);
	});

	it('connects, with the caller token, once the CA that signed the certificate is given', async () => {
		process.env[GATEWAY_URL_ENV] = base;
		process.env[CA_FILE_ENV] = join(dir, 'gw-ca.crt');
		const res = await asCaller(() => callGateway('ns', 'pilot', '/x'));
		expect(res?.status).toBe(200);
		expect(seen).toEqual(['Bearer callers-token']);
	});

	it('does not trust the CA for anything else the process connects to', async () => {
		process.env[GATEWAY_URL_ENV] = base;
		process.env[CA_FILE_ENV] = join(dir, 'gw-ca.crt');
		await asCaller(() => callGateway('ns', 'pilot', '/x'));
		await expect(fetch(`${base}/x`)).rejects.toThrow();
	});

	it('does not need the CA file for a gateway given as http', async () => {
		const fetchMock = vi.fn(async () => new Response('{}'));
		vi.stubGlobal('fetch', fetchMock);
		process.env[GATEWAY_URL_ENV] = 'http://gw.test/{namespace}/{crew}';
		process.env[CA_FILE_ENV] = '/nonexistent/ca.crt';
		await asCaller(() => callGateway('ns', 'pilot', '/x'));
		expect(fetchMock.mock.calls[0][0]).toBe('http://gw.test/ns/pilot/x');
		vi.unstubAllGlobals();
	});

	it('fails before any call when the gateway is https and the named CA file is missing', async () => {
		process.env[GATEWAY_URL_ENV] = base;
		process.env[CA_FILE_ENV] = '/nonexistent/ca.crt';
		await expect(asCaller(() => callGateway('ns', 'pilot', '/x'))).rejects.toThrow();
		expect(seen).toEqual([]);
	});
});
