import { render, screen, waitFor } from '@testing-library/svelte';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Component } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toKyaml } from '#lib/kyaml.js';
import { toYaml } from '#lib/yaml.js';
import { page, setPageUrl } from '../../vitest-mocks/app-state.svelte.js';

// Every Kubemoot kind a person can open on its own page (a Kubernetes node is not one; its page has its own test). A new detail page has to be
// added here and show the Description, YAML and JSON views; a new kind has to be added here or
// to NO_PAGE, so the choice is made on purpose.
const PAGES: Record<string, { route: string; kind: string }> = {
	agents: { route: '/agents/demo', kind: 'Agent' },
	crews: { route: '/crews/demo', kind: 'Crew' },
	crewfitnesses: { route: '/crewfitnesses/demo', kind: 'CrewFitness' },
	crewfitnesssuites: { route: '/crewfitnesssuites/demo', kind: 'CrewFitnessSuite' },
	embeddingmodels: { route: '/embeddingmodels/demo', kind: 'EmbeddingModel' },
	mcpcatalogs: { route: '/mcpcatalogs/demo', kind: 'MCPCatalog' },
	mcpgateways: { route: '/mcpgateways/demo', kind: 'MCPGateway' },
	mcpqualitypolicies: { route: '/mcpqualitypolicies/demo', kind: 'MCPQualityPolicy' },
	mcpreports: { route: '/mcpreports/demo', kind: 'MCPServerReport' },
	mcpservers: { route: '/mcpservers/demo', kind: 'MCPServer' },
	models: { route: '/models/demo', kind: 'Model' },
	modelservers: { route: '/modelservers/demo', kind: 'ModelServer' },
	promptmodules: { route: '/promptmodules/demo', kind: 'PromptModule' },
	ragsources: { route: '/ragsources/demo', kind: 'RAGSource' }
};
const SINGLETON_PAGES = { config: 'KubemootConfig' };
// Kinds with no page in the dashboard, so nothing to put a view on.
const NO_PAGE = ['CrewSchedulingPolicy', 'MootArchetype', 'NotificationSink', 'Skill'];

// vitest runs from the dashboard directory.
const routesDir = join(process.cwd(), 'src/routes');
const typesDir = join(process.cwd(), '../operator/api/v1alpha1');

const modules = import.meta.glob<{ default: Component }>(['./*/[[]name[]]/+page.svelte', './config/+page.svelte']);

// What a page reads without a guard, beyond the marker every fixture carries.
const REQUIRED_SPEC: Record<string, object> = { RAGSource: { source: { type: 'git' }, vectorStore: { type: 'qdrant' } } };

function fixture(kind: string) {
	return {
		apiVersion: 'kubemoot.ai/v1alpha1',
		kind,
		metadata: { name: 'demo', namespace: 'kubemoot', managedFields: [{ manager: 'kubemoot' }] },
		spec: { marker: 'spec-value', ...REQUIRED_SPEC[kind] },
		status: {}
	};
}

function stubFetch(kind: string) {
	const body = fixture(kind);
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: string) => {
			const url = input;
			const path = url.split('?')[0];
			const isConfig = path.endsWith('/config');
			const isCollection = /\/api\/kubemoot\/[a-z]+$/.test(path);
			let payload: object = body;
			if (isConfig) payload = { items: [body] };
			else if (isCollection) payload = { items: [] };
			return new Response(JSON.stringify(payload), { status: 200, headers: { 'content-type': 'application/json' } });
		})
	);
	return body;
}

beforeEach(() => {
	vi.stubGlobal('EventSource', class { addEventListener() {} close() {} });
});
afterEach(() => vi.unstubAllGlobals());

describe('which pages have the resource views', () => {
	it('lists every detail route in the dashboard', () => {
		const dirs = readdirSync(routesDir, { withFileTypes: true })
			.filter((d) => d.isDirectory() && d.name !== 'api' && d.name !== 'nodes' && existsSync(join(routesDir, d.name, '[name]', '+page.svelte')))
			.map((d) => d.name)
			.sort((a, b) => a.localeCompare(b));
		expect(dirs).toEqual(Object.keys(PAGES).sort((a, b) => a.localeCompare(b)));
	});

	it('accounts for every Kubemoot kind, with a page or a stated reason', () => {
		if (!existsSync(typesDir)) return;
		const kinds = new Set<string>();
		for (const file of readdirSync(typesDir).filter((f) => f.endsWith('_types.go'))) {
			const source = readFileSync(join(typesDir, file), 'utf8');
			// The root marker sits a few comment lines above the struct it marks.
			for (const m of source.matchAll(/\+kubebuilder:object:root=true\n(?:\/\/.*\n|\n)*?type (\w+) struct/g)) {
				if (!m[1].endsWith('List')) kinds.add(m[1]);
			}
		}
		const covered = new Set([...Object.values(PAGES).map((p) => p.kind), ...Object.values(SINGLETON_PAGES), ...NO_PAGE]);
		expect([...kinds].filter((k) => !covered.has(k))).toEqual([]);
		expect([...covered].filter((k) => !kinds.has(k))).toEqual([]);
	});
});

describe('every Kubemoot detail page offers Description, YAML, JSON and KYAML', () => {
	const entries = [
		...Object.entries(PAGES).map(([dir, p]) => ({ file: `./${dir}/[name]/+page.svelte`, route: p.route, kind: p.kind })),
		{ file: './config/+page.svelte', route: '/config', kind: SINGLETON_PAGES.config }
	];

	it.each(entries)('$kind ($route)', async ({ file, route, kind }) => {
		const body = stubFetch(kind);
		const load = modules[file];
		expect(load, `${file} is not found`).toBeTruthy();
		const Page = (await load()).default;
		setPageUrl(`${route}?namespace=kubemoot&view=yaml`);
		page.params = { name: 'demo' };

		const { container } = render(Page);
		await waitFor(() => expect(screen.getByRole('combobox', { name: 'View' })).toBeTruthy());
		const select = screen.getByRole<HTMLSelectElement>('combobox', { name: 'View' });
		expect([...select.options].map((o) => o.textContent)).toEqual(['Description', 'YAML', 'JSON', 'KYAML']);
		expect(select.value).toBe('yaml');

		const shown = container.querySelector('pre.code.yaml')?.textContent ?? '';
		const metadata = { name: body.metadata.name, namespace: body.metadata.namespace };
		expect(shown).toBe(toYaml({ ...body, metadata }));
		expect(shown).toContain(`kind: ${kind}`);
		expect(shown).toContain('marker: spec-value');
		expect(shown).not.toContain('managedFields');
		expect(body.metadata.managedFields).toHaveLength(1);

		// The same resource as JSON, from the same selector.
		setPageUrl(`${route}?namespace=kubemoot&view=json`);
		await waitFor(() => expect(container.querySelector('pre.code.json')).toBeTruthy());
		expect(JSON.parse(container.querySelector('pre.code.json')?.textContent ?? '')).toEqual({ ...body, metadata });

		// And as KYAML, the dialect `kubectl get -o kyaml` writes.
		setPageUrl(`${route}?namespace=kubemoot&view=kyaml`);
		await waitFor(() => expect(container.querySelector('pre.code.kyaml')).toBeTruthy());
		const kyaml = container.querySelector('pre.code.kyaml')?.textContent ?? '';
		expect(kyaml).toBe(toKyaml({ ...body, metadata }));
		expect(kyaml.startsWith('---\n{\n')).toBe(true);
		expect(kyaml).toContain(`kind: "${kind}"`);
	});
});
