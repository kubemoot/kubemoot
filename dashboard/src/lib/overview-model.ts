// What the Overview shows and why. Each tile answers one question, "is this part of Kubemoot
// working?", with a state from the shared health model, one figure, one line and, where a
// measurement helps, a small bar. The "Needs attention" list is every finding that is down or
// degraded, each pointing at the page that explains it. Pure: the page fetches, this decides.

import { clusterUse, cores, gib, nodeHealth, nodeProblems, nodeUse, type NodeMetric } from './node-usage.js';
import type { PodCount, Reading } from './reading.js';
import { nodeAccelerator } from './node-accelerator.js';
import { readyHealth, worstHealth, type Health } from './health.js';
import { nodeCounts } from './overview-counts.js';
import { acceleratorMemoryTotalMiB, acceleratorMemoryUsedMiB } from './accelerator-capacity.js';
import { formatMiB } from './format.js';
import { formatBytes, formatDuration, formatPercent, tileOf, type NatsStatus } from './nats/status.js';
import { threadActivity, type Thread } from './stores/discussions.js';
import type { NodeWithAccelerator } from './types/k8s.js';
import type { Agent, Crew, CrewFitnessSuite, EmbeddingModel, MCPGateway, MCPServer, Model, ModelServer, RAGSource } from './types/kubemoot.js';
import { bar, HIGH_USE_FRACTION, usageBar, type TileBar } from './usage-bar.js';
import { catalogEntries } from './model-catalog.js';
import { roleSearch } from './node-role.js';
import { operatorFacts, type Fact, type OperatorDetail } from './operator-detail.js';

/** A source the page reads: undefined while it loads, null when it could not be read, else the data. */
export type Source<T> = T | null | undefined;

export interface ComponentStatus {
	name: string;
	healthy: boolean;
	message: string;
}

/** The parts of a thread the stuck-discussion check reads. */
export type ThreadLike = Pick<Thread, 'threadId' | 'status' | 'userQuery' | 'startedAt' | 'messages'>;

export interface OverviewData {
	componentStatuses: Source<ComponentStatus[]>;
	/** The operator's version, uptime and CRDs as the signed-in person can read them. */
	operator: Source<OperatorDetail>;
	nodes: Source<NodeWithAccelerator[]>;
	/** CPU and memory use per node; null when metrics.k8s.io could not be read, with the reason. */
	metrics: Source<Reading<Record<string, NodeMetric>>>;
	pods: Source<Reading<Record<string, PodCount>>>;
	crews: Source<Crew[]>;
	modelServers: Source<ModelServer[]>;
	models: Source<Model[]>;
	embeddingModels: Source<EmbeddingModel[]>;
	mcpServers: Source<MCPServer[]>;
	mcpGateways: Source<MCPGateway[]>;
	ragSources: Source<RAGSource[]>;
	agents: Source<Agent[]>;
	suites: Source<CrewFitnessSuite[]>;
	nats: Source<NatsStatus>;
	/** The discussions the dashboard has seen; null when the bus could not be read. */
	threads: Source<ThreadLike[]>;
}

/** A place in the dashboard: a path, and the query string when the page needs one. */
export interface Link {
	path: string;
	search?: string;
}

/** Something that is wrong, and the page that explains it. */
export interface Finding {
	health: 'down' | 'degraded';
	/** What it is: "Node homelab-k8s-1-gpu-worker-3". */
	subject: string;
	/** What is wrong, in a few words. */
	problem: string;
	link: Link;
	/** What kind of resource it is, so many of one kind can be shown as one line. */
	group?: { noun: string; name: string; path: string };
}

export interface Tile {
	key: string;
	label: string;
	link: Link;
	/** loading: not read yet; unavailable: could not be read; ready: shows a state. */
	phase: 'loading' | 'unavailable' | 'ready';
	health: Health;
	figure: string;
	line: string;
	bars: TileBar[];
	/** Why a measurement is missing, when the tile otherwise has its numbers. */
	note?: string;
	/** Lines of fact under the figure, each a reading or the plain reason there is none. */
	details?: Fact[];
}

export interface Overview {
	tiles: Tile[];
	attention: Finding[];
}

/** An open discussion that has gone this long without a message is raised as stuck. A safety net, not a deadline. */
export const STUCK_AFTER_MS = 60 * 60 * 1000;

const SUITE_FAILED: Record<string, string> = { Failed: 'Failed', Error: 'Errored', ToolSmokeFailed: 'Tool smoke test failed' };
const MAX_SERVER_BARS = 4;

function encoded(name: string): string {
	return encodeURIComponent(name);
}

function detailLink(route: string, name: string, namespace?: string): Link {
	return { path: `${route}/${encoded(name)}`, search: namespace ? `?namespace=${encoded(namespace)}` : undefined };
}

type Listed = Judged[] | null | undefined;

/** One thing a tile counts: its state, and when it is wrong, the finding. */
interface Judged {
	health: Health;
	finding?: Finding;
}

function problem(health: 'down' | 'degraded', subject: string, text: string, link: Link, group?: Finding['group']): Judged {
	return { health, finding: { health, subject, problem: text, link, group } };
}

const OK: Judged = { health: 'healthy' };

/** A tile that is waiting for, or cannot get, its data. */
function pending(key: string, label: string, link: Link, source: unknown): Tile {
	const loading = source === undefined;
	return {
		key,
		label,
		link,
		phase: loading ? 'loading' : 'unavailable',
		health: 'unknown',
		figure: '',
		line: loading ? '' : 'Unavailable',
		bars: []
	};
}

/** "2 down, 1 degraded", or "All healthy" when nothing is wrong. */
function summaryLine(judged: readonly Judged[], allWell: string, idleLabel?: string): string {
	const count = (h: Health) => judged.filter((j) => j.health === h).length;
	const parts = [
		count('down') > 0 ? `${count('down')} down` : '',
		count('degraded') > 0 ? `${count('degraded')} degraded` : '',
		count('unknown') > 0 ? `${count('unknown')} unknown` : '',
		idleLabel && count('idle') > 0 ? `${count('idle')} ${idleLabel}` : ''
	].filter(Boolean);
	return parts.length > 0 ? parts.join(', ') : allWell;
}

/** A tile counting judged things: healthy (or idle) of all, in the worst state beneath it. */
function countTile(key: string, label: string, link: Link, judged: readonly Judged[], extra: Partial<Tile> = {}, idleLabel?: string): Tile {
	const well = judged.filter((j) => j.health === 'healthy' || j.health === 'idle').length;
	return {
		key,
		label,
		link,
		phase: 'ready',
		health: judged.length === 0 ? 'idle' : worstHealth(judged.map((j) => j.health)),
		figure: judged.length === 0 ? '0' : `${well}/${judged.length}`,
		line: judged.length === 0 ? 'None yet' : summaryLine(judged, 'All healthy', idleLabel),
		bars: [],
		...extra
	};
}

function nodeLink(name: string): Link {
	return { path: `/nodes/${encoded(name)}` };
}

function judgeNodes(nodes: NodeWithAccelerator[], metrics: Record<string, NodeMetric> | undefined): Judged[] {
	return nodes.map((node) => {
		const name = node.metadata.name;
		const problems = nodeProblems(node, nodeUse(node, metrics?.[name]));
		if (problems.length === 0) return OK;
		const health = nodeHealth(node, nodeUse(node, metrics?.[name]));
		// A node taken out on purpose is idle and is not listed as a problem.
		if (health === 'idle') return { health };
		return problem(health === 'down' ? 'down' : 'degraded', `Node ${name}`, problems.map((p) => p.text).join(', '), nodeLink(name));
	});
}

/** The accelerator tile opens the Nodes page on the accelerator nodes it counts. */
const ACCELERATOR_LINK = { path: '/nodes', search: roleSearch('accelerator') };

function nodeTiles(data: OverviewData): Tile[] {
	const link = { path: '/nodes', search: roleSearch('all') };
	const nodes = data.nodes;
	if (!nodes) return [pending('nodes', 'Nodes', link, nodes), pending('accelerators', 'Accelerators', ACCELERATOR_LINK, nodes)];
	const measured = data.metrics?.available ? data.metrics.data : undefined;
	const judged = judgeNodes(nodes, measured);
	const bars: TileBar[] = [];
	let note: string | undefined;
	if (measured) {
		const use = clusterUse(nodes, measured);
		if (use.cpu) bars.push(usageBar('CPU', use.cpu, cores, ' cores'));
		if (use.memory) bars.push(usageBar('Memory', use.memory, gib));
	} else if (data.metrics) {
		note = data.metrics.available ? undefined : data.metrics.reason;
	}
	const figure = `${nodeCounts(nodes).nodes.ready}/${nodes.length}`;
	return [countTile('nodes', 'Nodes', link, judged, { bars, note, figure }, 'offline (cordoned)'), acceleratorTile(nodes, judged)];
}

/** The accelerator devices on Ready nodes against all of them; the tile is as unwell as the worst accelerator node. */
function acceleratorTile(nodes: NodeWithAccelerator[], judged: readonly Judged[]): Tile {
	const link = ACCELERATOR_LINK;
	const { accelerators } = nodeCounts(nodes);
	if (accelerators.total === 0) {
		return { key: 'accelerators', label: 'Accelerators', link, phase: 'ready', health: 'idle', figure: '0', line: 'None in the cluster', bars: [] };
	}
	const states = nodes.flatMap((n, i) => (nodeAccelerator(n)?.present ? [judged[i].health] : []));
	const health = worstHealth(states);
	const line = accelerators.ready === accelerators.total ? 'All on Ready nodes' : `${accelerators.total - accelerators.ready} on nodes that are not Ready`;
	return { key: 'accelerators', label: 'Accelerators', link, phase: 'ready', health, figure: `${accelerators.ready}/${accelerators.total}`, line, bars: [] };
}

interface StatusLike {
	ready?: boolean;
	phase?: string;
	message?: string;
}

interface Named {
	metadata: { name: string; namespace?: string };
	status?: StatusLike;
}

/** "Not Ready (Pending)": the operator's message when it gave one. */
function notReadyText(status: StatusLike | undefined, fallback = 'Not Ready'): string {
	if (status?.message) return status.message;
	return status?.phase ? `${fallback} (${status.phase})` : fallback;
}

/**
 * One state per resource: ready is healthy (or whatever `whenReady` says), a failure phase is down,
 * anything else is degraded, with the operator's own words for why.
 */
function judgeResources<T extends Named>(
	items: readonly T[],
	noun: string,
	route: string,
	whenReady: (item: T) => Judged = () => OK
): Judged[] {
	return items.map((item) => {
		const health = readyHealth(item.status);
		if (health === 'healthy') return whenReady(item);
		const link = detailLink(route, item.metadata.name, item.metadata.namespace);
		return problem(health, `${noun} ${item.metadata.name}`, notReadyText(item.status), link, { noun, name: item.metadata.name, path: route });
	});
}

/** A model server that is ready but holds no model right now is idle: models load on demand. */
const idleWithoutModels = (s: ModelServer): Judged => ((s.status?.capacity?.loadedModels?.length ?? 0) === 0 ? { health: 'idle' } : OK);

/** An MCP server that reports Ready but has fewer replicas available than it asks for is degraded. */
function replicasShort(s: MCPServer): Judged {
	// A server the operator does not run (it points at an endpoint) has no replicas to count.
	if (s.spec.externalEndpoint) return OK;
	const wanted = s.spec.replicas ?? 0;
	const available = s.status?.availableReplicas ?? s.status?.replicas ?? 0;
	if (wanted <= 0 || available >= wanted) return OK;
	const link = detailLink('/mcpservers', s.metadata.name, s.metadata.namespace);
	return problem('degraded', `MCP server ${s.metadata.name}`, `Ready, but ${available} of ${wanted} replicas available`, link, { noun: 'MCP server', name: s.metadata.name, path: '/mcpservers' });
}

/** Accelerator memory in use per model server that reports its total. Servers that do not report it get no bar. */
function serverBars(servers: ModelServer[]): TileBar[] {
	const bars = servers.flatMap((s) => {
		const total = acceleratorMemoryTotalMiB(s.status?.capacity);
		const used = acceleratorMemoryUsedMiB(s.status?.capacity);
		if (!total || total <= 0 || used === undefined) return [];
		return [{ ...bar(s.metadata.name, `${formatMiB(used)} of ${formatMiB(total)}`, used / total), level: 'normal' as const }];
	});
	return bars.slice(0, MAX_SERVER_BARS);
}

/** A Model resource as the generic judge reads it: its state is its phase. */
function modelResource(m: Model): Named {
	return { metadata: m.metadata, status: { ready: m.status?.ready, phase: m.status?.state, message: m.status?.message } };
}

/** A catalog entry a server lists as not available right now is degraded. */
function judgeCatalog(servers: ModelServer[]): Judged[] {
	return catalogEntries(servers).map((e) => {
		if (e.model.present !== false) return OK;
		const search = `?model=${encoded(e.model.name)}&server=${encoded(e.server)}`;
		return problem('degraded', `Model ${e.model.name}`, `Not available on ${e.server}`, { path: '/models', search });
	});
}

/**
 * Models are the Model resources plus the catalogs the model servers report, as the Models page lists
 * them. Still loading while either is; unavailable only when neither could be read.
 */
function modelsJudged({ models, modelServers }: OverviewData): Listed {
	if (models === undefined || modelServers === undefined) return undefined;
	if (models === null && modelServers === null) return null;
	return [...judgeResources((models ?? []).map(modelResource), 'Model', '/models'), ...judgeCatalog(modelServers ?? [])];
}

/** The operator's own page: version, uptime, health, leader and the registered CRDs. */
const OPERATOR_LINK = { path: '/operator' };

function operatorDetails(operator: Source<OperatorDetail>, now: number): Fact[] {
	if (operator === undefined) return [];
	if (operator === null) return [{ text: 'Version, uptime and CRDs could not be read', read: false }];
	return operatorFacts(operator, now);
}

/** The tile when the operator's health report is missing but its Deployment could be read: what is known, with the report marked unavailable. */
function operatorWithoutReport(operator: Source<OperatorDetail>, now: number): Tile {
	return { key: 'operator', label: 'Operator', link: OPERATOR_LINK, phase: 'ready', health: 'unknown', figure: '', line: 'Health report unavailable', bars: [], details: operatorDetails(operator, now) };
}

function operatorTile(statuses: Source<ComponentStatus[]>, operator: Source<OperatorDetail>, now: number): Tile {
	const link = OPERATOR_LINK;
	if (statuses === undefined) return pending('operator', 'Operator', link, statuses);
	const status = statuses?.find((c) => c.name === 'operator');
	if (!status) return operator ? operatorWithoutReport(operator, now) : pending('operator', 'Operator', link, null);
	return {
		key: 'operator',
		label: 'Operator',
		link,
		phase: 'ready',
		health: status.healthy ? 'healthy' : 'down',
		figure: status.healthy ? 'Ready' : 'Not ready',
		line: status.healthy ? '' : status.message,
		bars: [],
		details: operatorDetails(operator, now)
	};
}

/** Components the operator reports that have a tile of their own (NATS, model servers) are not repeated here. */
function componentFindings(statuses: ComponentStatus[]): Finding[] {
	const link = { path: '/config' };
	return statuses
		.filter((c) => !c.healthy && c.name !== 'nats' && c.name !== 'modelservers')
		.map((c) =>
			c.name === 'operator'
				? { health: 'down' as const, subject: 'Operator', problem: c.message || 'Not ready', link: OPERATOR_LINK }
				: { health: 'degraded' as const, subject: `Component ${c.name}`, problem: c.message || 'Not healthy', link }
		);
}

function natsFindings(status: NatsStatus): Finding[] {
	const tile = tileOf(status);
	if (!tile) return [];
	const link = { path: '/nats' };
	if (tile.state === 'disconnected') return [{ health: 'down', subject: 'NATS', problem: tile.error ?? 'Not connected', link }];
	const stores: Finding[] = tile.warnings.map((w) => ({
		health: 'degraded',
		subject: `NATS stream ${w.store}`,
		problem: `${formatPercent(w.fraction)} of its ${w.kind} limit`,
		link
	}));
	const accountFull = [tile.storage, tile.memory].some((u) => u?.fraction != null && u.fraction >= HIGH_USE_FRACTION);
	return stores.length === 0 && accountFull ? [{ health: 'degraded', subject: 'NATS', problem: 'Storage or memory is near its limit', link }] : stores;
}

function natsBars(tile: NonNullable<ReturnType<typeof tileOf>>): TileBar[] {
	const storage = tile.storage;
	if (!storage || storage.fraction === null || storage.limit === null) return [];
	return [bar('File storage', `${formatBytes(storage.used)} of ${formatBytes(storage.limit)}`, storage.fraction)];
}

function natsTile(status: Source<NatsStatus>): Tile {
	const link = { path: '/nats' };
	const tile = tileOf(status ?? null);
	if (!status || !tile) return pending('nats', 'NATS', link, status);
	const base = { key: 'nats', label: 'NATS', link, phase: 'ready' as const };
	if (tile.state === 'disconnected') {
		return { ...base, health: 'down', figure: 'Down', line: tile.error ?? 'Not connected', bars: [] };
	}
	const warned = natsFindings(status).length;
	return {
		...base,
		health: warned > 0 ? 'degraded' : 'healthy',
		figure: String(tile.streams),
		line: warned > 0 ? `stores, ${warned} near a limit` : `stores, server ${tile.version ?? 'unknown'}`,
		bars: natsBars(tile)
	};
}

/** A failed suite is raised only while it is the newest suite of its crew and finished this recently. */
export const SUITE_FAILURE_WINDOW_MS = 24 * 60 * 60 * 1000;

const suiteCrew = (s: CrewFitnessSuite) => `${s.metadata.namespace ?? ''}/${s.spec.crewRef}`;
const startedOf = (s: CrewFitnessSuite) => Date.parse(s.metadata.creationTimestamp ?? s.status?.startedAt ?? '') || 0;
const finishedOf = (s: CrewFitnessSuite) => Date.parse(s.status?.completedAt ?? s.status?.startedAt ?? s.metadata.creationTimestamp ?? '') || 0;

function newestPerCrew(suites: CrewFitnessSuite[]): CrewFitnessSuite[] {
	const newest = new Map<string, CrewFitnessSuite>();
	for (const s of suites) {
		const known = newest.get(suiteCrew(s));
		if (!known || startedOf(s) > startedOf(known)) newest.set(suiteCrew(s), s);
	}
	return [...newest.values()];
}

function suiteFindings(suites: CrewFitnessSuite[], now: number): Finding[] {
	return newestPerCrew(suites)
		.filter((s) => s.status?.phase && s.status.phase in SUITE_FAILED && now - finishedOf(s) < SUITE_FAILURE_WINDOW_MS)
		.map((s) => ({
			health: 'degraded' as const,
			subject: `Fitness suite ${s.metadata.name}`,
			problem: s.status?.error || SUITE_FAILED[s.status?.phase ?? ''],
			link: detailLink('/crewfitnesssuites', s.metadata.name, s.metadata.namespace)
		}));
}

function shorten(text: string, max: number): string {
	return text.length > max ? `${text.slice(0, max - 1)}...` : text;
}

function stuckSubject(question: string | undefined): string {
	const text = (question ?? '').trim();
	return text ? 'Discussion "' + shorten(text, 48) + '"' : 'A discussion';
}

function stuckFindings(threads: ThreadLike[], now: number): Finding[] {
	return threads
		.filter((t) => t.status === 'open')
		.flatMap((t) => {
			const last = threadActivity(t);
			if (last <= 0 || now - last < STUCK_AFTER_MS) return [];
			const quiet = formatDuration(Math.round((now - last) / 1000));
			const link = { path: '/moot', search: `?thread=${encoded(t.threadId)}` };
			return [{ health: 'degraded' as const, subject: stuckSubject(t.userQuery), problem: `Open, no message for ${quiet}`, link }];
		});
}

function findingsOf(judged: readonly Judged[]): Finding[] {
	return judged.flatMap((j) => (j.finding ? [j.finding] : []));
}

/** What each group of resources is judged to be. undefined while a source loads, null when it could not be read. */
interface Judgments {
	nodes: Listed;
	crews: Listed;
	servers: Listed;
	models: Listed;
	embeddings: Listed;
	mcp: Listed;
	gateways: Listed;
	rag: Listed;
	agents: Listed;
}

function over<T>(source: Source<T[]>, judge: (items: T[]) => Judged[]): Listed {
	return source ? judge(source) : source;
}

function judgeAll(data: OverviewData): Judgments {
	const measured = data.metrics?.available ? data.metrics.data : undefined;
	return {
		nodes: over(data.nodes, (n) => judgeNodes(n, measured)),
		crews: over(data.crews, (c) => judgeResources(c, 'Crew', '/crews')),
		servers: over(data.modelServers, (s) => judgeResources(s, 'Model server', '/modelservers', idleWithoutModels)),
		models: modelsJudged(data),
		embeddings: over(data.embeddingModels, (m) => judgeResources(m, 'Embedding model', '/embeddingmodels')),
		mcp: over(data.mcpServers, (s) => judgeResources(s, 'MCP server', '/mcpservers', replicasShort)),
		gateways: over(data.mcpGateways, (g) => judgeResources(g, 'MCP gateway', '/mcpgateways')),
		rag: over(data.ragSources, (r) => judgeResources(r, 'RAG source', '/ragsources')),
		agents: over(data.agents, (a) => judgeResources(a, 'Agent', '/agents'))
	};
}

function tileFor(key: string, label: string, path: string, listed: Listed, extra: Partial<Tile> = {}): Tile {
	const link = { path };
	return listed ? countTile(key, label, link, listed, extra) : pending(key, label, link, listed);
}

function tilesOf(data: OverviewData, j: Judgments, now: number): Tile[] {
	return [
		operatorTile(data.componentStatuses, data.operator, now),
		...nodeTiles(data),
		tileFor('crews', 'Crews', '/crews', j.crews),
		tileFor('modelservers', 'Model servers', '/modelservers', j.servers, { bars: serverBars(data.modelServers ?? []) }),
		tileFor('models', 'Models', '/models', j.models),
		tileFor('embeddingmodels', 'Embeddings', '/embeddingmodels', j.embeddings),
		tileFor('mcpservers', 'MCP servers', '/mcpservers', j.mcp),
		tileFor('mcpgateways', 'MCP gateways', '/mcpgateways', j.gateways),
		tileFor('ragsources', 'RAG sources', '/ragsources', j.rag),
		tileFor('agents', 'Agents', '/agents', j.agents),
		natsTile(data.nats)
	];
}

function findingsFor(data: OverviewData, j: Judgments, now: number): Finding[] {
	const resources = Object.values(j).flatMap((listed) => listed ?? []);
	return [
		...(data.componentStatuses ? componentFindings(data.componentStatuses) : []),
		...findingsOf(resources),
		...(data.nats ? natsFindings(data.nats) : []),
		...(data.suites ? suiteFindings(data.suites, now) : []),
		...(data.threads ? stuckFindings(data.threads, now) : [])
	];
}

/** From this many of one kind in one state, the list shows one line for them. */
export const COLLAPSE_FROM = 4;

function collapseGroup(noun: string, path: string, members: Finding[]): Finding {
	const names = members.map((m) => m.group?.name ?? m.subject);
	const shown = names.slice(0, 3).join(', ');
	const rest = names.length > 3 ? ` and ${names.length - 3} more` : '';
	const same = members.every((m) => m.problem === members[0].problem);
	const health = members.some((m) => m.health === 'down') ? 'down' : 'degraded';
	return { health, subject: `${members.length} ${noun}s`, problem: `${same ? members[0].problem + ': ' : ''}${shown}${rest}`, link: { path } };
}

/** Many resources of one kind with trouble become one line that opens the list; the rest stay as they are. */
export function collapseFindings(findings: Finding[]): Finding[] {
	const groups = new Map<string, Finding[]>();
	for (const f of findings) {
		if (!f.group) continue;
		const key = `${f.group.noun}|${f.health}`;
		groups.set(key, [...(groups.get(key) ?? []), f]);
	}
	const collapsed = [...groups.values()].filter((g) => g.length >= COLLAPSE_FROM);
	const folded = new Set(collapsed.flat());
	return [...findings.filter((f) => !folded.has(f)), ...collapsed.map((g) => collapseGroup(g[0].group!.noun, g[0].group!.path, g))];
}

/** The Overview: its tiles in order, and what needs attention, down before degraded. */
export function buildOverview(data: OverviewData, now: number = Date.now()): Overview {
	const judgments = judgeAll(data);
	const attention = collapseFindings(findingsFor(data, judgments, now)).toSorted((a, b) => Number(b.health === 'down') - Number(a.health === 'down'));
	return { tiles: tilesOf(data, judgments, now), attention };
}

export type { TileBar } from './usage-bar.js';
export type { Reading } from './reading.js';
