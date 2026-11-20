// @vitest-environment node
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// The dashboard server only reads. Its Kubernetes and NATS calls are scanned here so a
// write added later fails the test: no create, patch, replace or delete of a Kubernetes
// object, no NATS publish, purge, stream change, or key-value and object-store write. The
// short-lived consumers the history and stream routes open to read (`consumers.add` and
// `consumers.delete`) are allowed: they carry no data.

const SRC = fileURLToPath(new URL('.', import.meta.url));
const SERVER_DIRS = [join(SRC, 'lib', 'server'), join(SRC, 'routes', 'api')];

const WRITE_CALLS: { name: string; pattern: RegExp }[] = [
	{ name: 'Kubernetes create/patch/replace/delete', pattern: /\.(create|patch|replace|delete|connectPost|connectPut)(Namespaced|Cluster)?(CustomObject|Namespace|Pod|ConfigMap|Secret|Job|Deployment)\w*\(/ },
	{ name: 'NATS publish', pattern: /\.publish\(/ },
	{ name: 'NATS purge', pattern: /\.purge\(/ },
	{ name: 'key-value or object-store write', pattern: /\b(kv|os|store|bucket)\w*\.(put|create|update|delete)\(/i },
	{ name: 'JetStream stream management', pattern: /\.streams\.(add|update|delete)\(/ }
];

function serverSources(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) return serverSources(path);
		return entry.name.endsWith('.ts') && !entry.name.endsWith('.test.ts') ? [path] : [];
	});
}

function writesIn(text: string): string[] {
	return WRITE_CALLS.filter(({ pattern }) => pattern.test(text)).map(({ name }) => name);
}

describe('writesIn', () => {
	it('flags each kind of write', () => {
		expect(writesIn('api.patchNamespacedCustomObject({})')).toHaveLength(1);
		expect(writesIn('api.deleteNamespacedCustomObject({})')).toHaveLength(1);
		expect(writesIn('nc.publish(subject, data)')).toHaveLength(1);
		expect(writesIn('jsm.streams.purge(name)')).toHaveLength(1);
		expect(writesIn('kv.put(key, value)')).toHaveLength(1);
		expect(writesIn('kv.delete(key)')).toHaveLength(1);
		expect(writesIn('jsm.streams.add(config)')).toHaveLength(1);
	});

	it('passes reads and the removal of a read consumer', () => {
		expect(writesIn('api.listNamespacedCustomObject({})')).toEqual([]);
		expect(writesIn('api.getNamespacedCustomObject({})')).toEqual([]);
		expect(writesIn('kv.get(key)')).toEqual([]);
		expect(writesIn('await jsm.consumers.delete(stream, name)')).toEqual([]);
		expect(writesIn('await jsm.consumers.add(stream, config)')).toEqual([]);
	});
});

describe('dashboard server code', () => {
	it('makes no Kubernetes or NATS write', () => {
		const offenders = SERVER_DIRS.flatMap(serverSources).flatMap((file) =>
			writesIn(readFileSync(file, 'utf8')).map((name) => `${relative(SRC, file)}: ${name}`)
		);
		expect(offenders).toEqual([]);
	});

	it('finds the server sources it scans', () => {
		expect(SERVER_DIRS.flatMap(serverSources).length).toBeGreaterThan(20);
	});
});
