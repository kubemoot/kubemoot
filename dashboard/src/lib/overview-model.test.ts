import { describe, expect, it } from 'vitest';
import { buildOverview, STUCK_AFTER_MS, type OverviewData, type Tile } from './overview-model';
import type { NatsStatus, StoreStatus } from './nats/status';

const NOW = Date.parse('2026-11-22T18:00:00Z');
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const HOUR = 3_600_000;

const meta = (name: string, namespace = 'kubemoot') => ({ name, namespace });
const rdy = { ready: true, phase: 'Ready' };

const node = (name: string, over: { ready?: boolean; conditions?: object[]; accelerator?: object } = {}) => ({
	metadata: { name },
	spec: {},
	status: {
		conditions: over.conditions ?? [{ type: 'Ready', status: over.ready === false ? 'False' : 'True', reason: over.ready === false ? 'KubeletNotReady' : 'KubeletReady' }],
		allocatable: { cpu: '8', memory: '16Gi', pods: '110' }
	},
	accelerator: over.accelerator
});

const empty: OverviewData = {
	componentStatuses: [],
	operator: undefined,
	nodes: [],
	metrics: { available: true, data: {} },
	pods: { available: true, data: {} },
	acceleratorUse: undefined,
	crews: [],
	modelServers: [],
	models: [],
	embeddingModels: [],
	mcpServers: [],
	mcpGateways: [],
	ragSources: [],
	agents: [],
	suites: [],
	nats: undefined,
	threads: []
};

const data = (over: Partial<OverviewData>): OverviewData => ({ ...empty, ...over });
const tile = (o: ReturnType<typeof buildOverview>, key: string): Tile => o.tiles.find((t) => t.key === key) as Tile;

const server = (name: string, status: object = { ...rdy, capacity: { loadedModels: [{ name: 'm' }] } }) => ({ metadata: meta(name), spec: { type: 'vllm' }, status });

describe('the tiles', () => {
	it('lists one tile per question, NATS as a single cell like the others', () => {
		const o = buildOverview(data({ componentStatuses: [{ name: 'operator', healthy: true, message: 'ok' }] }), NOW);
		expect(o.tiles.map((t) => t.key)).toEqual([
			'operator', 'nodes', 'accelerators', 'crews', 'modelservers', 'models', 'embeddingmodels', 'mcpservers', 'mcpgateways', 'ragsources', 'agents', 'nats'
		]);
	});

	it('is loading before a source is read and unavailable, never zero, when it could not be', () => {
		const o = buildOverview(data({ crews: undefined, agents: null }), NOW);
		expect(tile(o, 'crews')).toMatchObject({ phase: 'loading', health: 'unknown', figure: '' });
		expect(tile(o, 'agents')).toMatchObject({ phase: 'unavailable', health: 'unknown', figure: '', line: 'Unavailable' });
	});

	it('shows ready-of-total from the resources and takes the worst state beneath it', () => {
		const crews = [
			{ metadata: meta('a'), spec: {}, status: rdy },
			{ metadata: meta('b'), spec: {}, status: { ready: false, phase: 'Pending' } },
			{ metadata: meta('c'), spec: {}, status: { ready: false, phase: 'Error' } }
		];
		const t = tile(buildOverview(data({ crews: crews as never }), NOW), 'crews');
		expect(t).toMatchObject({ figure: '1/3', health: 'down', line: '1 down, 1 degraded' });
	});

	it('is healthy when all are ready and idle when there are none', () => {
		const one = [{ metadata: meta('a'), spec: {}, status: rdy }];
		expect(tile(buildOverview(data({ crews: one as never }), NOW), 'crews')).toMatchObject({ figure: '1/1', health: 'healthy', line: 'All healthy' });
		expect(tile(buildOverview(data({}), NOW), 'crews')).toMatchObject({ figure: '0', health: 'idle' });
	});

	it('counts models as the Models page lists them: Model resources plus the catalogs of the model servers', () => {
		const servers = [
			server('rig0', { ...rdy, models: [{ name: 'qwen3:32b', present: true }] }),
			server('rig1', { ...rdy, models: [{ name: 'qwen3:14b' }] }),
			server('cpu', { ...rdy, models: [] })
		];
		const t = tile(buildOverview(data({ modelServers: servers as never }), NOW), 'models');
		expect(t).toMatchObject({ figure: '2/2', health: 'healthy' });
	});

	it('raises a model a server lists as not available', () => {
		const servers = [server('rig0', { ...rdy, models: [{ name: 'a', present: true }, { name: 'b', present: false }] })];
		const o = buildOverview(data({ modelServers: servers as never }), NOW);
		expect(tile(o, 'models')).toMatchObject({ figure: '1/2', health: 'degraded' });
		expect(o.attention).toContainEqual(expect.objectContaining({ subject: 'Model b', problem: 'Not available on rig0', link: { path: '/models', search: '?model=b&server=rig0' } }));
	});

	it('is loading until both Model resources and servers are read, and unavailable only when neither could be', () => {
		expect(tile(buildOverview(data({ models: undefined }), NOW), 'models').phase).toBe('loading');
		expect(tile(buildOverview(data({ models: null, modelServers: null }), NOW), 'models').phase).toBe('unavailable');
		expect(tile(buildOverview(data({ models: null }), NOW), 'models').phase).toBe('ready');
	});

	it('counts a ready model server with nothing loaded as idle, not as a problem', () => {
		const t = tile(buildOverview(data({ modelServers: [server('rig0', { ...rdy, capacity: { loadedModels: [] } })] as never }), NOW), 'modelservers');
		expect(t).toMatchObject({ figure: '1/1', health: 'idle' });
	});

	it('draws accelerator memory per model server that reports it, and none for a server that does not', () => {
		const servers = [
			server('rig0', { ...rdy, capacity: { acceleratorMemoryUsedMiB: 16384, acceleratorMemoryTotalMiB: 32768 } }),
			server('cpu', { ...rdy, capacity: {} })
		];
		const t = tile(buildOverview(data({ modelServers: servers as never }), NOW), 'modelservers');
		expect(t.bars).toEqual([{ label: 'rig0', text: '16.0 GiB of 32.0 GiB', fraction: 0.5, level: 'normal' }]);
	});
});

describe('the Nodes and Accelerators tiles', () => {
	const metrics = (cpu: string, memory: string) => ({ available: true as const, data: { n1: { cpu, memory }, n2: { cpu, memory } } });

	it('shows CPU and memory use of the whole cluster as small bars', () => {
		const o = buildOverview(data({ nodes: [node('n1'), node('n2')] as never, metrics: metrics('4', '8Gi') }), NOW);
		expect(tile(o, 'nodes').figure).toBe('2/2');
		expect(tile(o, 'nodes').bars.map((b) => [b.label, b.fraction])).toEqual([['CPU', 0.5], ['Memory', 0.5]]);
	});

	it('says why use is missing when metrics cannot be read, and draws no bar', () => {
		const t = tile(buildOverview(data({ nodes: [node('n1')] as never, metrics: { available: false, reason: 'Node metrics (metrics.k8s.io) is not served by this cluster' } }), NOW), 'nodes');
		expect(t.bars).toEqual([]);
		expect(t.note).toContain('not served');
	});

	it('is down with the node named when a node is NotReady, and degraded when it is under pressure', () => {
		const nodes = [node('n1'), node('n2', { ready: false }), node('n3', { conditions: [{ type: 'Ready', status: 'True' }, { type: 'MemoryPressure', status: 'True' }] })];
		const o = buildOverview(data({ nodes: nodes as never }), NOW);
		expect(tile(o, 'nodes')).toMatchObject({ figure: '2/3', health: 'down', line: '1 down, 1 degraded' });
		expect(o.attention.map((f) => [f.health, f.subject, f.problem, f.link.path])).toEqual([
			['down', 'Node n2', 'Not Ready (KubeletNotReady)', '/nodes/n2'],
			['degraded', 'Node n3', 'MemoryPressure', '/nodes/n3']
		]);
	});

	it('takes the accelerator tile from the nodes that carry accelerators, without listing the node twice', () => {
		const gpu = { present: true, count: 1 };
		const nodes = [node('cpu'), node('gpu1', { accelerator: gpu }), node('gpu2', { ready: false, accelerator: gpu })];
		const o = buildOverview(data({ nodes: nodes as never }), NOW);
		expect(tile(o, 'accelerators')).toMatchObject({ figure: '1/2', health: 'down', line: '1 not available: node offline' });
		expect(o.attention.filter((f) => f.subject === 'Node gpu2')).toHaveLength(1);
	});

	it('draws busy and memory on the accelerator tile only when the metrics source reports them, from the available accelerators', () => {
		const gpu = { present: true, count: 1 };
		const nodes = [node('gpu1', { accelerator: gpu }), node('gpu2', { accelerator: gpu }), node('gpu3', { ready: false, accelerator: gpu })];
		const use = {
			configured: true as const,
			use: { available: true as const, data: { gpu1: { devices: 1, busy: 1, memoryUsedMiB: 10240, memoryTotalMiB: 20480 }, gpu2: { devices: 1, busy: 0, memoryUsedMiB: 0, memoryTotalMiB: 20480 }, gpu3: { devices: 1, busy: 1, memoryUsedMiB: 20480, memoryTotalMiB: 20480 } } }
		};
		const bars = tile(buildOverview(data({ nodes: nodes as never, acceleratorUse: use }), NOW), 'accelerators').bars;
		expect(bars.map((b) => [b.label, b.fraction])).toEqual([['Busy', 0.5], ['Memory', 0.25]]);
		expect(bars.every((b) => b.level === 'normal')).toBe(true);
	});

	it('draws no accelerator bars, and no zeros, when no source is set or it failed', () => {
		const nodes = [node('gpu1', { accelerator: { present: true, count: 1 } })];
		for (const acceleratorUse of [undefined, null, { configured: false as const }, { configured: true as const, use: { available: false as const, reason: 'down' } }]) {
			expect(tile(buildOverview(data({ nodes: nodes as never, acceleratorUse }), NOW), 'accelerators').bars).toEqual([]);
		}
	});

	it('is idle with no accelerators in the cluster', () => {
		expect(tile(buildOverview(data({ nodes: [node('n1')] as never }), NOW), 'accelerators')).toMatchObject({ figure: '0', health: 'idle' });
	});
});

describe('where each tile opens', () => {
	const o = buildOverview(data({ componentStatuses: [{ name: 'operator', healthy: true, message: 'ok' }], nodes: [node('n1')] as never }), NOW);
	const link = (key: string) => tile(o, key).link;

	it('opens the most specific view of what the tile counts', () => {
		expect(link('operator')).toEqual({ path: '/operator' });
		expect(link('nodes')).toEqual({ path: '/nodes', search: '?role=all' });
		expect(link('accelerators')).toEqual({ path: '/nodes', search: '?role=accelerator' });
		expect(link('crews')).toEqual({ path: '/crews' });
		expect(link('modelservers')).toEqual({ path: '/modelservers' });
		expect(link('models')).toEqual({ path: '/models' });
		expect(link('embeddingmodels')).toEqual({ path: '/embeddingmodels' });
		expect(link('mcpservers')).toEqual({ path: '/mcpservers' });
		expect(link('mcpgateways')).toEqual({ path: '/mcpgateways' });
		expect(link('ragsources')).toEqual({ path: '/ragsources' });
		expect(link('agents')).toEqual({ path: '/agents' });
		expect(link('nats')).toEqual({ path: '/nats' });
	});

	it('keeps the accelerator link while the nodes load or cannot be read', () => {
		for (const nodes of [undefined, null]) {
			const waiting = buildOverview(data({ nodes }), NOW);
			expect(tile(waiting, 'accelerators').link).toEqual({ path: '/nodes', search: '?role=accelerator' });
			expect(tile(waiting, 'nodes').link).toEqual({ path: '/nodes', search: '?role=all' });
		}
	});
});

describe('the Operator tile', () => {
	const up = [{ name: 'operator', healthy: true, message: 'ok' }];
	const deployment = { available: true as const, data: { name: 'kubemoot-operator', namespace: 'kubemoot', version: '0.356.0', image: 'i', desired: 1, ready: 1, conditions: [], resource: {} } };
	const detail = (over: object = {}) => ({ deployment, startedAt: { available: true as const, data: ago(30 * HOUR) }, leader: { available: true as const, data: 'p' }, crds: { available: true as const, data: [{ name: 'a.kubemoot.ai', kind: 'A', scope: 'Namespaced', versions: [] }] }, ...over });
	const details = (operator: OverviewData['operator']) => tile(buildOverview(data({ componentStatuses: up, operator }), NOW), 'operator').details;

	it('shows the version, uptime and number of CRDs under Ready', () => {
		const t = tile(buildOverview(data({ componentStatuses: up, operator: detail() }), NOW), 'operator');
		expect(t).toMatchObject({ figure: 'Ready', health: 'healthy', line: '' });
		expect(t.details).toEqual([
			{ text: 'Version 0.356.0', read: true },
			{ text: 'Up 1d 6h', read: true },
			{ text: '1 Kubemoot CRD', read: true }
		]);
	});

	it('says plainly which line the account may not read, and never shows a zero for it', () => {
		const refused = { available: false as const, reason: 'your account may not read the custom resource definitions', denied: true };
		const lines = details(detail({ crds: refused }))!;
		expect(lines[2]).toEqual({ text: 'Kubemoot CRDs: your account may not read them', read: false });
		expect(lines.map((l) => l.text).join()).not.toMatch(/\b0 Kubemoot/);
		const noDeployment = { available: false as const, reason: 'your account may not read the operator Deployment', denied: true };
		expect(details(detail({ deployment: noDeployment }))![0]).toEqual({ text: 'Version: your account may not read them', read: false });
	});

	it('says no pod runs rather than a zero uptime', () => {
		expect(details(detail({ startedAt: { available: true, data: '' } }))![1]).toEqual({ text: 'Uptime: no operator pod is running', read: true });
	});

	it('shows nothing under the figure while the operator is read, and one plain line when it could not be', () => {
		expect(details(undefined)).toEqual([]);
		expect(details(null)).toEqual([{ text: 'Version, uptime and CRDs could not be read', read: false }]);
	});

	it('still shows what could be read when the health report cannot be, and is unavailable only when nothing could', () => {
		for (const statuses of [null, [] as never]) {
			const t = tile(buildOverview(data({ componentStatuses: statuses, operator: detail() }), NOW), 'operator');
			expect(t).toMatchObject({ phase: 'ready', health: 'unknown', figure: '', line: 'Health report unavailable' });
			expect(t.details?.[0]).toEqual({ text: 'Version 0.356.0', read: true });
		}
		expect(tile(buildOverview(data({ componentStatuses: null, operator: null }), NOW), 'operator')).toMatchObject({ phase: 'unavailable', line: 'Unavailable' });
		expect(tile(buildOverview(data({ componentStatuses: undefined, operator: detail() }), NOW), 'operator').phase).toBe('loading');
	});

	it('shows the operator message when it is not ready', () => {
		const down = buildOverview(data({ componentStatuses: [{ name: 'operator', healthy: false, message: 'leader lost' }], operator: detail() }), NOW);
		expect(tile(down, 'operator')).toMatchObject({ figure: 'Not ready', line: 'leader lost' });
	});
});

describe('needs attention', () => {
	it('is empty when everything is fine', () => {
		expect(buildOverview(data({ componentStatuses: [{ name: 'operator', healthy: true, message: 'ok' }] }), NOW).attention).toEqual([]);
	});

	it('names an operator that is not ready, and the Operator tile is down', () => {
		const o = buildOverview(data({ componentStatuses: [{ name: 'operator', healthy: false, message: 'leader lost' }] }), NOW);
		expect(tile(o, 'operator')).toMatchObject({ health: 'down', figure: 'Not ready' });
		expect(o.attention).toEqual([{ health: 'down', subject: 'Operator', problem: 'leader lost', link: { path: '/operator' } }]);
	});

	it('names a model server that is down, with the page that explains it', () => {
		const o = buildOverview(data({ modelServers: [server('rig0', { ready: false, phase: 'Failed', message: 'engine unreachable' })] as never }), NOW);
		expect(o.attention).toEqual([
			{ health: 'down', subject: 'Model server rig0', problem: 'engine unreachable', link: { path: '/modelservers/rig0', search: '?namespace=kubemoot' }, group: { noun: 'Model server', name: 'rig0', path: '/modelservers' } }
		]);
	});

	it('names a crew that is not Ready', () => {
		const o = buildOverview(data({ crews: [{ metadata: meta('pilot', 'crew-pilot'), spec: {}, status: { ready: false, phase: 'Pending' } }] as never }), NOW);
		expect(o.attention[0]).toMatchObject({ health: 'degraded', subject: 'Crew pilot', problem: 'Not Ready (Pending)', link: { path: '/crews/pilot', search: '?namespace=crew-pilot' } });
	});

	it('names an MCP server that is Ready with fewer replicas than it asks for', () => {
		const mcp = [{ metadata: meta('k8s'), spec: { replicas: 2 }, status: { ready: true, availableReplicas: 0 } }];
		const o = buildOverview(data({ mcpServers: mcp as never }), NOW);
		expect(tile(o, 'mcpservers')).toMatchObject({ figure: '0/1', health: 'degraded' });
		expect(o.attention[0]).toMatchObject({ subject: 'MCP server k8s', problem: 'Ready, but 0 of 2 replicas available' });
	});

	it('leaves alone an MCP server that has all the replicas it asks for, or manages none', () => {
		const mcp = [
			{ metadata: meta('a'), spec: { replicas: 2 }, status: { ready: true, availableReplicas: 2 } },
			{ metadata: meta('b'), spec: {}, status: { ready: true } },
			// An external server is not run by the operator, so it has no replicas to count.
			{ metadata: meta('c'), spec: { replicas: 2, externalEndpoint: 'https://example.invalid/mcp' }, status: { ready: true } }
		];
		expect(buildOverview(data({ mcpServers: mcp as never }), NOW).attention).toEqual([]);
	});

	it('names a failed fitness suite and ignores a completed one', () => {
		const done = ago(2 * HOUR);
		const suites = [
			{ metadata: meta('bad', 'crew-x'), spec: { crewRef: 'a' }, status: { phase: 'Failed', error: 'judge unreachable', completedAt: done } },
			{ metadata: meta('smoke', 'crew-x'), spec: { crewRef: 'b' }, status: { phase: 'ToolSmokeFailed', completedAt: done } },
			{ metadata: meta('good', 'crew-x'), spec: { crewRef: 'c' }, status: { phase: 'Completed', completedAt: done } }
		];
		const o = buildOverview(data({ suites: suites as never }), NOW);
		expect(o.attention.map((f) => [f.subject, f.problem])).toEqual([
			['Fitness suite bad', 'judge unreachable'],
			['Fitness suite smoke', 'Tool smoke test failed']
		]);
		expect(o.attention[0].link).toEqual({ path: '/crewfitnesssuites/bad', search: '?namespace=crew-x' });
	});

	it('names an open discussion with no message for over an hour, and not a recent or a finished one', () => {
		const thread = (id: string, status: string, last: string) => ({ threadId: id, status, userQuery: 'What are the two GPU temperatures?', startedAt: last, messages: [{ timestamp: last }] });
		const threads = [thread('old', 'open', ago(STUCK_AFTER_MS + 26 * HOUR)), thread('fresh', 'open', ago(5 * 60_000)), thread('done', 'synthesized', ago(30 * HOUR))];
		const o = buildOverview(data({ threads: threads as never }), NOW);
		expect(o.attention).toHaveLength(1);
		expect(o.attention[0]).toMatchObject({ health: 'degraded', subject: 'Discussion "What are the two GPU temperatures?"', link: { path: '/moot', search: '?thread=old' } });
		expect(o.attention[0].problem).toMatch(/^Open, no message for (1 day|27 hours)/);
	});

	it('lists down before degraded', () => {
		const o = buildOverview(
			data({
				crews: [{ metadata: meta('c'), spec: {}, status: { ready: false, phase: 'Pending' } }] as never,
				nodes: [node('n1', { ready: false })] as never
			}),
			NOW
		);
		expect(o.attention.map((f) => f.health)).toEqual(['down', 'degraded']);
	});
});

describe('failed fitness suites', () => {
	const suite = (name: string, crew: string, phase: string, created: string, completedAt?: string) => ({
		metadata: { ...meta(name, 'crew-x'), creationTimestamp: created },
		spec: { crewRef: crew },
		status: { phase, completedAt }
	});

	it('raises a failure that is the newest suite of its crew and finished within a day', () => {
		const o = buildOverview(data({ suites: [suite('s1', 'a', 'Failed', ago(3 * HOUR), ago(2 * HOUR))] as never }), NOW);
		expect(o.attention.map((f) => f.subject)).toEqual(['Fitness suite s1']);
	});

	it('drops a failure that finished more than a day ago', () => {
		const o = buildOverview(data({ suites: [suite('s1', 'a', 'Failed', ago(30 * HOUR), ago(25 * HOUR))] as never }), NOW);
		expect(o.attention).toEqual([]);
	});

	it('drops a failure once a newer suite of the same crew exists, whatever its result', () => {
		const suites = [suite('old', 'a', 'Failed', ago(5 * HOUR), ago(4 * HOUR)), suite('new', 'a', 'Completed', ago(2 * HOUR), ago(HOUR))];
		expect(buildOverview(data({ suites: suites as never }), NOW).attention).toEqual([]);
	});

	it('keeps a failure of one crew when only another crew has a newer suite', () => {
		const suites = [suite('a1', 'a', 'Failed', ago(5 * HOUR), ago(4 * HOUR)), suite('b1', 'b', 'Completed', ago(2 * HOUR), ago(HOUR))];
		expect(buildOverview(data({ suites: suites as never }), NOW).attention.map((f) => f.subject)).toEqual(['Fitness suite a1']);
	});
});

describe('a node taken out on purpose', () => {
	const cordonedOff = { ...node('gpu-3', { ready: false }), spec: { unschedulable: true } };

	it('is Idle and offline, is not listed, and does not turn the Nodes or Accelerators tiles red', () => {
		const gpu = { present: true, count: 1 };
		const o = buildOverview(data({ nodes: [node('n1'), { ...cordonedOff, accelerator: gpu }, node('gpu-2', { accelerator: gpu })] as never }), NOW);
		expect(o.attention).toEqual([]);
		expect(tile(o, 'nodes')).toMatchObject({ health: 'healthy', figure: '2/3', line: '1 offline (cordoned)' });
		expect(tile(o, 'accelerators').health).toBe('healthy');
	});

	it('stays Down when the node is NotReady and not cordoned', () => {
		const o = buildOverview(data({ nodes: [node('n1'), node('n2', { ready: false })] as never }), NOW);
		expect(tile(o, 'nodes').health).toBe('down');
		expect(o.attention.map((f) => f.subject)).toEqual(['Node n2']);
	});

	it('is still Degraded when cordoned but Ready', () => {
		const o = buildOverview(data({ nodes: [{ ...node('n1'), spec: { unschedulable: true } }] as never }), NOW);
		expect(o.attention).toEqual([expect.objectContaining({ health: 'degraded', problem: 'Cordoned, no new pods' })]);
	});
});

describe('many of one kind', () => {
	const agent = (n: number, ok = false) => ({ metadata: meta(`agent-${n}`), spec: {}, status: ok ? rdy : { ready: false, phase: 'Pending' } });

	it('shows three as three lines', () => {
		const o = buildOverview(data({ agents: [1, 2, 3].map((n) => agent(n)) as never }), NOW);
		expect(o.attention).toHaveLength(3);
	});

	it('shows four or more as one line that opens the list, naming the first few', () => {
		const o = buildOverview(data({ agents: [1, 2, 3, 4, 5, 6].map((n) => agent(n)) as never }), NOW);
		expect(o.attention).toEqual([
			{ health: 'degraded', subject: '6 Agents', problem: 'Not Ready (Pending): agent-1, agent-2, agent-3 and 3 more', link: { path: '/agents' } }
		]);
		expect(tile(o, 'agents')).toMatchObject({ figure: '0/6', health: 'degraded' });
	});

	it('keeps the healthy ones out of the line and takes the worst state', () => {
		const agents = [...[1, 2, 3, 4].map((n) => agent(n)), agent(9, true), { ...agent(5), status: { ready: false, phase: 'Error' } }];
		const o = buildOverview(data({ agents: agents as never }), NOW);
		expect(o.attention).toHaveLength(2);
		expect(o.attention.map((f) => f.health)).toEqual(['down', 'degraded']);
	});
});

describe('NATS', () => {
	const store = (over: object): StoreStatus =>
		({
			definition: null,
			name: 'KV_x',
			kind: 'kv',
			limits: { maxAgeSeconds: null, maxBytes: 100, maxMessages: null, history: null, discard: 'new' },
			usage: { messages: 1, bytes: 99, firstSeq: 1, lastSeq: 1, consumers: 0, keys: null },
			...over
		});
	const status = (stores: StoreStatus[], connected = true): NatsStatus => ({
		connected,
		checkedAt: new Date(NOW).toISOString(),
		server: { version: '2.12.0', name: 'n', uptime: null, connections: null },
		account: { storage: { used: 10, limit: 100 }, memory: { used: 0, limit: null }, streams: { used: 1, limit: null }, consumers: { used: 0, limit: null } },
		stores,
		absent: [],
		error: connected ? undefined : 'dial tcp: no route to host'
	});

	it('is one tile with the store count and the storage bar, healthy within its limits', () => {
		const t = tile(buildOverview(data({ nats: status([store({ usage: { messages: 1, bytes: 10, firstSeq: 1, lastSeq: 1, consumers: 0, keys: null } })]) }), NOW), 'nats');
		expect(t).toMatchObject({ health: 'healthy', figure: '1', phase: 'ready' });
		expect(t.bars[0]).toMatchObject({ label: 'File storage', fraction: 0.1 });
	});

	it('raises a stream at its cap and makes the tile degraded', () => {
		const o = buildOverview(data({ nats: status([store({})]) }), NOW);
		expect(tile(o, 'nats')).toMatchObject({ health: 'degraded' });
		expect(o.attention).toEqual([{ health: 'degraded', subject: 'NATS stream KV_x', problem: '99% of its bytes limit', link: { path: '/nats' } }]);
	});

	it('is down when not connected, with the reason', () => {
		const o = buildOverview(data({ nats: status([], false) }), NOW);
		expect(tile(o, 'nats')).toMatchObject({ health: 'down', figure: 'Down', line: 'dial tcp: no route to host' });
		expect(o.attention[0]).toMatchObject({ health: 'down', subject: 'NATS' });
	});
});
