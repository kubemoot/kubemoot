// A call that carries the signed-in person's bearer token inside the cluster goes over https and
// is verified against the install CA, which the chart mounts as a file. The CA is used for these
// calls only: each one is its own https request with the CA as its only trust, so nothing else the
// process connects to (the identity provider, Prometheus, the Kubernetes API) trusts it.
// Node's own fetch has no per-call trust store without a dependency, so this is a thin
// node:https request that returns a standard Response, streaming included.

import { readFileSync, statSync } from 'node:fs';
import https from 'node:https';
import { Readable } from 'node:stream';

import { caFile } from './mode.js';

/** The headers, body and signal of a call; the same fields fetch takes. */
export interface InternalInit {
	method?: string;
	headers?: Record<string, string>;
	body?: string;
	signal?: AbortSignal | null;
}

let loaded: { file: string; mtimeMs: number; size: number; pem: string } | null = null;

/**
 * The CA bundle in the file, read again when it changes. Throws when the file is unreadable or
 * holds no certificate, so a call that would carry a token fails instead of going unverified.
 */
export function readCA(file: string): string {
	const info = statSync(file);
	if (loaded && loaded.file === file && loaded.mtimeMs === info.mtimeMs && loaded.size === info.size) return loaded.pem;
	const pem = readFileSync(file, 'utf8');
	if (!pem.includes('BEGIN CERTIFICATE')) throw new Error(`the CA file ${file} holds no certificate`);
	loaded = { file, mtimeMs: info.mtimeMs, size: info.size, pem };
	return pem;
}

/** True for an https address, whatever the case of the scheme; the one rule both the check and the warning use. */
export function isHttps(url: string): boolean {
	return url.toLowerCase().startsWith('https:');
}

/** True for a plain http address, whatever the case of the scheme. */
export function isPlainHttp(url: string): boolean {
	return url.toLowerCase().startsWith('http:');
}

/** Forgets the CA that was read; tests use it. */
export function resetCA(): void {
	loaded = null;
}

const NO_BODY = new Set([101, 204, 205, 304]);

function toHeaders(raw: Record<string, string | string[] | undefined>): Headers {
	const headers = new Headers();
	for (const [name, value] of Object.entries(raw)) {
		if (value === undefined) continue;
		for (const v of [value].flat()) headers.append(name, v);
	}
	return headers;
}

function request(url: string, init: InternalInit, ca: string): Promise<Response> {
	return new Promise((resolve, reject) => {
		const req = https.request(url, { method: init.method ?? 'GET', headers: init.headers, ca, signal: init.signal ?? undefined }, (res) => {
			const status = res.statusCode ?? 502;
			const body = NO_BODY.has(status) ? null : (Readable.toWeb(res) as ReadableStream<Uint8Array>);
			if (!body) res.resume();
			resolve(new Response(body, { status, statusText: res.statusMessage, headers: toHeaders(res.headers) }));
		});
		req.on('error', reject);
		if (init.body !== undefined) req.write(init.body);
		req.end();
	});
}

/**
 * Calls url like fetch does. An https url with a CA file configured
 * is verified against that CA alone and never follows a redirect; any other url goes through the ordinary fetch (an http
 * address an administrator chose, or https with no CA configured, which uses the system roots).
 */
export async function internalFetch(url: string, init: InternalInit = {}, file: string = caFile()): Promise<Response> {
	if (!file || !isHttps(url)) return fetch(url, init);
	return request(url, init, readCA(file));
}
