// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { expectedOrigin, sameOrigin, type OriginContext } from './same-origin';

function ctx(over: Partial<Omit<OriginContext, 'url' | 'headers'>> & { url?: string; headers?: Record<string, string> } = {}): OriginContext {
	return {
		url: new URL(over.url ?? 'https://localhost:18090/dashboard/signin'),
		headers: new Headers(over.headers ?? {}),
		configuredOrigin: over.configuredOrigin,
		protocolHeader: over.protocolHeader,
		encrypted: over.encrypted
	};
}

const post = (origin?: string) => new Request('https://x.test/signin', { method: 'POST', headers: origin ? { origin } : {} });

describe('expectedOrigin without a configured origin', () => {
	it('is http for a plain connection, as a kubectl port-forward is, though adapter-node guessed https', () => {
		expect(expectedOrigin(ctx({ encrypted: false }))).toBe('http://localhost:18090');
	});

	it('is https for a TLS connection', () => {
		expect(expectedOrigin(ctx({ url: 'http://dash.example.test/x', encrypted: true }))).toBe('https://dash.example.test');
	});

	it('follows the request URL when the server cannot tell (the dev server)', () => {
		expect(expectedOrigin(ctx({ url: 'http://localhost:5173/dashboard' }))).toBe('http://localhost:5173');
	});

	it('uses the scheme a trusted proxy forwards over the plain connection behind it', () => {
		const c = ctx({ url: 'https://dash.example.test/x', encrypted: false, protocolHeader: 'x-forwarded-proto', headers: { 'x-forwarded-proto': 'https' } });
		expect(expectedOrigin(c)).toBe('https://dash.example.test');
		const first = ctx({ url: 'https://d.test/x', encrypted: false, protocolHeader: 'x-forwarded-proto', headers: { 'x-forwarded-proto': 'http, https' } });
		expect(expectedOrigin(first)).toBe('http://d.test');
	});

	it('ignores the forwarded scheme when no proxy is trusted, so a client cannot choose it', () => {
		expect(expectedOrigin(ctx({ encrypted: false, headers: { 'x-forwarded-proto': 'https' } }))).toBe('http://localhost:18090');
	});

	it('ignores a forwarded value that is not a scheme and falls back to the connection', () => {
		const c = ctx({ encrypted: false, protocolHeader: 'x-forwarded-proto', headers: { 'x-forwarded-proto': 'evil.test/' } });
		expect(expectedOrigin(c)).toBe('http://localhost:18090');
	});
});

describe('expectedOrigin with a configured origin', () => {
	it('is the configured origin whatever the connection is', () => {
		const c = ctx({ url: 'https://dash.example.test/x', configuredOrigin: 'https://dash.example.test', encrypted: false });
		expect(expectedOrigin(c)).toBe('https://dash.example.test');
	});
});

describe('sameOrigin', () => {
	it('accepts the expected origin only', () => {
		const expected = 'http://localhost:18090';
		expect(sameOrigin(post('http://localhost:18090'), expected)).toBe(true);
		expect(sameOrigin(post('https://localhost:18090'), expected)).toBe(false);
		expect(sameOrigin(post('http://evil.example.test'), expected)).toBe(false);
		expect(sameOrigin(post('http://localhost:18091'), expected)).toBe(false);
		expect(sameOrigin(post(), expected)).toBe(false);
	});
});
