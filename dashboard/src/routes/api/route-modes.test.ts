// Route coverage for the two deployment switches, so a route added later cannot
// slip past either one:
//   - every exported non-GET handler under src/routes/api is refused by the hook
//     in read-only mode;
//   - every route file either uses the shared scope helper ($lib/server/scope) or
//     is listed below as cluster-scoped on purpose, with the reason.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { handle } from '../../hooks.server';

const modules = import.meta.glob<Record<string, unknown>>('./**/+server.ts', { eager: true });
const sources = import.meta.glob<string>('./**/+server.ts', {
	eager: true,
	query: '?raw',
	import: 'default'
});

const METHODS = ['GET', 'HEAD', 'OPTIONS', 'POST', 'PUT', 'PATCH', 'DELETE'] as const;
const WRITE_EXPORTS = ['POST', 'PUT', 'PATCH', 'DELETE', 'fallback'];

/** Routes that do not use the scope helper, and why that is correct. */
const CLUSTER_SCOPED: Record<string, string> = {
	'./health/+server.ts': 'liveness of the dashboard and API server connection; no namespaced data',
	'./version/+server.ts': 'the dashboard build version',
	'./nodes/+server.ts': 'nodes are cluster-scoped infrastructure; attendees can read them with kubectl',
	'./kubemoot/config/+server.ts': 'KubemootConfig is a cluster-scoped singleton (image versions, defaults)',
	'./kubemoot/componentstatuses/+server.ts': 'operator control-plane health: name, healthy, message',
	'./kubemoot/system-info/+server.ts': 'operator version and uptime',
	'./nats/config/+server.ts': 'whether NATS is configured; no subjects or data',
};

const WRITE_ROUTES = [
	['POST', './nats/publish/+server.ts'],
	['POST', './nats/purge/+server.ts'],
	['POST', './kubemoot/crew-memory/+server.ts'],
	['DELETE', './kubemoot/crew-memory/+server.ts'],
	['POST', './discussions/pin/+server.ts'],
	['DELETE', './discussions/pin/+server.ts'],
	['POST', './kubemoot/modelproviders/[name]/load/+server.ts'],
	['POST', './kubemoot/modelproviders/[name]/unload/+server.ts'],
	['POST', './kubemoot/modelproviders/[name]/delete/+server.ts'],
	['PATCH', './kubemoot/crewfitnesssuites/[namespace]/[name]/+server.ts'],
	['DELETE', './kubemoot/crewfitnesssuites/[namespace]/[name]/+server.ts'],
	['PATCH', './kubemoot/mcpserverreports/[name]/+server.ts']
] as const;

function writeHandlers(): { file: string; method: string }[] {
	return Object.entries(modules).flatMap(([file, mod]) =>
		WRITE_EXPORTS.filter((name) => name in mod).map((method) => ({ file, method }))
	);
}

function eventFor(method: string, path = '/dashboard/api/anything') {
	return { request: new Request(`http://dashboard${path}`, { method }) } as Parameters<typeof handle>[0]['event'];
}

async function run(method: string, path?: string) {
	const resolve = vi.fn(async () => new Response('route ran'));
	const response = await handle({ event: eventFor(method, path), resolve });
	return { response, resolve };
}

afterEach(() => {
	vi.unstubAllEnvs();
});

describe('route enumeration', () => {
	it('finds the route files and the known write handlers', () => {
		expect(Object.keys(modules).length).toBeGreaterThan(40);
		const found = new Set(writeHandlers().map((h) => `${h.method} ${h.file}`));
		for (const [method, file] of WRITE_ROUTES) {
			expect(found, `${method} ${file}`).toContain(`${method} ${file}`);
		}
	});

	it('finds no write handler beyond the known ones', () => {
		const known = new Set(WRITE_ROUTES.map(([m, f]) => `${m} ${f}`));
		const unknown = writeHandlers()
			.map((h) => `${h.method} ${h.file}`)
			.filter((h) => !known.has(h));
		expect(unknown, 'a new write route: add it to WRITE_ROUTES and check its UI control').toEqual([]);
	});
});

describe('read-only mode', () => {
	it.each(writeHandlers())('refuses $method on $file', async ({ file, method }) => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', 'true');
		const { response, resolve } = await run(method, file.replace('./', '/dashboard/api/'));
		expect(response.status).toBe(403);
		expect(response.headers.get('Allow')).toBe('GET, HEAD, OPTIONS');
		expect(await response.json()).toMatchObject({ readOnly: true, error: expect.stringContaining('read-only') });
		expect(resolve).not.toHaveBeenCalled();
	});

	it.each(['POST', 'PUT', 'PATCH', 'DELETE', 'CUSTOM'])('refuses %s on any path', async (method) => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', '1');
		const { response, resolve } = await run(method, '/dashboard/discussions');
		expect(response.status).toBe(403);
		expect(resolve).not.toHaveBeenCalled();
	});

	it.each(['GET', 'HEAD', 'OPTIONS'])('lets %s through to the route', async (method) => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', 'true');
		const { response, resolve } = await run(method);
		expect(response.status).toBe(200);
		expect(resolve).toHaveBeenCalledOnce();
	});

	it.each(['true', 'TRUE', ' yes ', '1', 'on'])('turns on for %j', async (value) => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', value);
		expect((await run('POST')).response.status).toBe(403);
	});
});

describe('read-only mode off', () => {
	it.each(['', 'false', '0', 'no', 'off', 'maybe'])('changes nothing for %j', async (value) => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', value);
		for (const method of METHODS) {
			const { response, resolve } = await run(method);
			expect(await response.text(), method).toBe('route ran');
			expect(resolve).toHaveBeenCalledOnce();
		}
	});

	it('changes nothing when the variable is unset', async () => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', undefined);
		const { response } = await run('DELETE');
		expect(await response.text()).toBe('route ran');
	});
});

describe('namespace scope coverage', () => {
	it('every route uses the scope helper or is cluster-scoped on purpose', () => {
		const unguarded = Object.entries(sources)
			.filter(([file, text]) => !text.includes('$lib/server/scope') && !(file in CLUSTER_SCOPED))
			.map(([file]) => file);
		expect(unguarded, 'use $lib/server/scope in these routes, or list them in CLUSTER_SCOPED with a reason').toEqual([]);
	});

	it('lists no cluster-scoped route that is gone or already uses the helper', () => {
		for (const file of Object.keys(CLUSTER_SCOPED)) {
			expect(sources[file], `${file} no longer exists`).toBeDefined();
			expect(sources[file], `${file} uses the scope helper; drop it from CLUSTER_SCOPED`).not.toContain(
				'$lib/server/scope'
			);
		}
	});

	it('gives every cluster-scoped route a reason', () => {
		for (const reason of Object.values(CLUSTER_SCOPED)) expect(reason.length).toBeGreaterThan(10);
	});
});
