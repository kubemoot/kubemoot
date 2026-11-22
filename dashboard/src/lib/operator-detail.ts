// What the operator page and the Overview's Operator tile show about the operator: its version,
// how long it has been up, its health, which pod holds the leader lease, and the Kubemoot CRDs
// registered in the cluster. Each part is read on its own as the signed-in person, so one the
// account may not read says so and never shows a zero. Pure: the server reads, this decides.

import type { Reading } from './reading.js';

export const KUBEMOOT_GROUP = 'kubemoot.ai';

export interface OperatorCondition {
	type: string;
	status: string;
	reason?: string;
	message?: string;
	lastTransitionTime?: string;
}

export interface OperatorDeployment {
	name: string;
	namespace: string;
	/** The app.kubernetes.io/version label of the Deployment: the version the chart stamped. */
	version: string;
	image: string;
	desired: number;
	ready: number;
	createdAt?: string;
	conditions: OperatorCondition[];
	/** The Deployment as the API server returned it, for the YAML, JSON and KYAML views. */
	resource: object;
}

export interface OperatorCrdVersion {
	name: string;
	served: boolean;
	storage: boolean;
}

export interface OperatorCrd {
	name: string;
	kind: string;
	scope: string;
	versions: OperatorCrdVersion[];
}

export interface OperatorDetail {
	deployment: Reading<OperatorDeployment>;
	/** ISO start time of the oldest running operator pod; empty when none runs. */
	startedAt: Reading<string>;
	/** The operator pod that holds the leader lease; empty when the lease names none of them. */
	leader: Reading<string>;
	crds: Reading<OperatorCrd[]>;
}

export interface DeploymentLike {
	metadata?: { name?: string; namespace?: string; labels?: Record<string, string>; creationTimestamp?: string | Date };
	spec?: { replicas?: number; template?: { spec?: { containers?: { image?: string }[] } } };
	status?: { readyReplicas?: number; conditions?: (Omit<OperatorCondition, 'lastTransitionTime'> & { lastTransitionTime?: string | Date })[] };
}

const isoOf = (t: string | Date | undefined): string | undefined => (t === undefined ? undefined : new Date(t).toISOString());

/** A time as the API server writes it: whole seconds, "2026-05-18T05:34:49Z". */
const apiTime = (t: Date): string => t.toISOString().replace(/\.000Z$/, 'Z');

/**
 * The Deployment as plain JSON, as the API server sent it. The client library turns timestamps into
 * Dates, which would print with milliseconds; they are written back as the server writes them.
 */
function plainResource(dep: DeploymentLike): object {
	return JSON.parse(
		JSON.stringify(dep, function (this: Record<string, unknown>, key: string, value: unknown) {
			const original = this[key];
			return original instanceof Date ? apiTime(original) : value;
		})
	) as object;
}

function conditionsOf(dep: DeploymentLike): OperatorCondition[] {
	return (dep.status?.conditions ?? []).map((c) => ({ ...c, lastTransitionTime: isoOf(c.lastTransitionTime) }));
}

/** The version label the chart stamps on the operator Deployment; 'unknown' when it has none. */
export const deploymentVersion = (dep: DeploymentLike): string => dep.metadata?.labels?.['app.kubernetes.io/version'] || 'unknown';

/** The image of the Deployment's first container; 'unknown' when it names none. */
export const deploymentImage = (dep: DeploymentLike): string => dep.spec?.template?.spec?.containers?.[0]?.image ?? 'unknown';

/** The label selector of a Deployment's pods, from its matchLabels; empty when it has none. */
export function podSelectorOf(dep: { spec?: { selector?: { matchLabels?: Record<string, string> } } }): string {
	return Object.entries(dep.spec?.selector?.matchLabels ?? {})
		.map(([k, v]) => `${k}=${v}`)
		.join(',');
}

function identityOf(dep: DeploymentLike): Pick<OperatorDeployment, 'name' | 'namespace' | 'createdAt'> {
	return { name: dep.metadata?.name ?? '', namespace: dep.metadata?.namespace ?? '', createdAt: isoOf(dep.metadata?.creationTimestamp) };
}

/** The operator Deployment as the page shows it, 'unknown' where the Deployment names no version or image. */
export function operatorDeployment(dep: DeploymentLike): OperatorDeployment {
	return {
		...identityOf(dep),
		version: deploymentVersion(dep),
		image: deploymentImage(dep),
		desired: dep.spec?.replicas ?? 1,
		ready: dep.status?.readyReplicas ?? 0,
		conditions: conditionsOf(dep),
		resource: plainResource(dep)
	};
}

interface CrdLike {
	metadata?: { name?: string };
	spec?: { group?: string; scope?: string; names?: { kind?: string }; versions?: { name?: string; served?: boolean; storage?: boolean }[] };
}

/** The CRDs of the Kubemoot group, by name; every other group's CRDs are left out. */
export function kubemootCrds(items: readonly CrdLike[]): OperatorCrd[] {
	return items
		.filter((c) => c.spec?.group === KUBEMOOT_GROUP)
		.map((c) => ({
			name: c.metadata?.name ?? '',
			kind: c.spec?.names?.kind ?? '',
			scope: c.spec?.scope ?? '',
			versions: (c.spec?.versions ?? []).map((v) => ({ name: v.name ?? '', served: v.served === true, storage: v.storage === true }))
		}))
		.sort((a, b) => a.name.localeCompare(b.name));
}

interface LeaseLike {
	spec?: { holderIdentity?: string };
}

/**
 * The operator pod that holds the leader lease. The holder is written "<pod name>_<id>"; a lease
 * whose holder is not one of the operator's pods (named after the Deployment) is some other lease.
 */
export function leaderPod(leases: readonly LeaseLike[], deploymentName: string): string {
	for (const lease of leases) {
		const pod = (lease.spec?.holderIdentity ?? '').split('_')[0];
		if (pod.startsWith(`${deploymentName}-`)) return pod;
	}
	return '';
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** How long something has been up, in its two largest units: "3d 4h", "5h 12m", "42m", "30s". Empty when unknown. */
export function formatUptime(ms: number): string {
	if (!Number.isFinite(ms) || ms < 0) return '';
	if (ms >= DAY) return `${Math.floor(ms / DAY)}d ${Math.floor((ms % DAY) / HOUR)}h`;
	if (ms >= HOUR) return `${Math.floor(ms / HOUR)}h ${Math.floor((ms % HOUR) / MINUTE)}m`;
	if (ms >= MINUTE) return `${Math.floor(ms / MINUTE)}m`;
	return `${Math.floor(ms / 1000)}s`;
}

/** One line a person reads: what it says, and whether it is a reading or the reason there is none. */
export interface Fact {
	text: string;
	read: boolean;
}

/** A refusal in the short form a tile has room for ("your account may not read them"); any other reason stays whole. */
const refused = (label: string, reading: { available: false; reason: string; denied?: boolean }): Fact => ({
	text: `${label}: ${reading.denied ? 'your account may not read them' : reading.reason}`,
	read: false
});

function versionFact(d: OperatorDetail['deployment']): Fact {
	return d.available ? { text: `Version ${d.data.version}`, read: true } : refused('Version', d);
}

function uptimeFact(s: OperatorDetail['startedAt'], now: number): Fact {
	if (!s.available) return refused('Uptime', s);
	if (s.data === '') return { text: 'Uptime: no operator pod is running', read: true };
	return { text: `Up ${formatUptime(now - Date.parse(s.data))}`, read: true };
}

function crdFact(c: OperatorDetail['crds']): Fact {
	if (!c.available) return refused('Kubemoot CRDs', c);
	return { text: `${c.data.length} Kubemoot CRD${c.data.length === 1 ? '' : 's'}`, read: true };
}

/** The three lines of the Operator tile: version, uptime and registered CRDs. */
export function operatorFacts(detail: OperatorDetail, now: number): Fact[] {
	return [versionFact(detail.deployment), uptimeFact(detail.startedAt, now), crdFact(detail.crds)];
}
