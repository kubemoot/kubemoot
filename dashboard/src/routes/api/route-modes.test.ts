// Route coverage, so a route added later cannot slip past either rule:
//   - the dashboard is read-only: no route under src/routes/api exports a handler that
//     changes state, every former write route is gone, and the server hook refuses any
//     non-GET method with 405;
//   - every route file either uses the shared scope helper (#lib/server/scope) or
//     is listed below as cluster-scoped on purpose, with the reason.
import { describe, expect, it, vi } from 'vitest';
import { handle } from '../../hooks.server';

const modules = import.meta.glob<Record<string, unknown>>('./**/+server.ts', { eager: true });
const sources = import.meta.glob<string>('./**/+server.ts', {
	eager: true,
	query: '?raw',
	import: 'default'
});

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
	'./kubemoot/discussions/artifact/+server.ts': 'redirects to the workspace route, which applies the scope',
};

/** Every write route the dashboard once had: the file and the method it served. */
const FORMER_WRITE_ROUTES = [
	['POST', './nats/publish/+server.ts', '/nats/publish'],
	['POST', './nats/purge/+server.ts', '/nats/purge'],
	['POST', './kubemoot/crew-memory/+server.ts', '/kubemoot/crew-memory'],
	['DELETE', './kubemoot/crew-memory/+server.ts', '/kubemoot/crew-memory'],
	['POST', './discussions/pin/+server.ts', '/discussions/pin'],
	['DELETE', './discussions/pin/+server.ts', '/discussions/pin'],
	['POST', './kubemoot/modelservers/[name]/load/+server.ts', '/kubemoot/modelservers/x/load'],
	['POST', './kubemoot/modelservers/[name]/unload/+server.ts', '/kubemoot/modelservers/x/unload'],
	['POST', './kubemoot/modelservers/[name]/delete/+server.ts', '/kubemoot/modelservers/x/delete'],
	['PATCH', './kubemoot/crewfitnesssuites/[namespace]/[name]/+server.ts', '/kubemoot/crewfitnesssuites/ns/x'],
	['DELETE', './kubemoot/crewfitnesssuites/[namespace]/[name]/+server.ts', '/kubemoot/crewfitnesssuites/ns/x'],
	['DELETE', './kubemoot/crews/[name]/+server.ts', '/kubemoot/crews/x'],
	['PATCH', './kubemoot/mcpserverreports/[name]/+server.ts', '/kubemoot/mcpserverreports/x']
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

describe('route enumeration', () => {
	it('finds the route files', () => {
		expect(Object.keys(modules).length).toBeGreaterThan(30);
	});

	it('finds no route that exports a write handler', () => {
		expect(writeHandlers().map((h) => `${h.method} ${h.file}`)).toEqual([]);
	});
});

describe('former write routes', () => {
	it.each(FORMER_WRITE_ROUTES)('%s %s no longer exists as a handler', (method, file) => {
		expect(modules[file] === undefined || !(method in modules[file])).toBe(true);
	});

	it.each(FORMER_WRITE_ROUTES)('%s %s is refused with 405 before any route runs', async (method, _file, path) => {
		const { response, resolve } = await run(method, `/dashboard/api${path}`);
		expect(response.status).toBe(405);
		expect(response.headers.get('Allow')).toBe('GET, HEAD, OPTIONS');
		expect(await response.json()).toMatchObject({ readOnly: true, error: expect.stringContaining('read-only') });
		expect(resolve).not.toHaveBeenCalled();
	});
});

describe('read-only hook', () => {
	it.each(['POST', 'PUT', 'PATCH', 'DELETE', 'CUSTOM'])('refuses %s on any path', async (method) => {
		const { response, resolve } = await run(method, '/dashboard/discussions');
		expect(response.status).toBe(405);
		expect(resolve).not.toHaveBeenCalled();
	});

	it.each(['GET', 'HEAD', 'OPTIONS'])('lets %s through to the route', async (method) => {
		const { response, resolve } = await run(method);
		expect(response.status).toBe(200);
		expect(resolve).toHaveBeenCalledOnce();
	});
});

describe('namespace scope coverage', () => {
	it('every route uses the scope helper or is cluster-scoped on purpose', () => {
		const unguarded = Object.entries(sources)
			.filter(([file, text]) => !text.includes('#lib/server/scope.js') && !(file in CLUSTER_SCOPED))
			.map(([file]) => file);
		expect(unguarded, 'use #lib/server/scope.js in these routes, or list them in CLUSTER_SCOPED with a reason').toEqual([]);
	});

	it('lists no cluster-scoped route that is gone or already uses the helper', () => {
		for (const file of Object.keys(CLUSTER_SCOPED)) {
			expect(sources[file], `${file} no longer exists`).toBeDefined();
			expect(sources[file], `${file} uses the scope helper; drop it from CLUSTER_SCOPED`).not.toContain(
				'#lib/server/scope.js'
			);
		}
	});

	it('gives every cluster-scoped route a reason', () => {
		for (const reason of Object.values(CLUSTER_SCOPED)) expect(reason.length).toBeGreaterThan(10);
	});
});
