// How the NATS status page words its figures.
import type { CrewRetentionStatus } from '#lib/types/kubemoot.js';
import { OWNER_LABELS, PEOPLES_DATA_LABELS } from './stores.js';
import { formatAge, formatBytes, formatLimit, formatPercent, fraction, historyFill, type StoreStatus } from './status.js';

export interface StoreRow {
	name: string;
	kind: string;
	holds: string;
	owner: string;
	peoplesData: string;
	holdsPeoplesData: boolean;
	messages: string;
	bytes: string;
	/** Share of the byte limit in use, '' when the store has no byte limit. */
	bytesShare: string;
	maxAge: string;
	maxBytes: string;
	maxMessages: string;
	history: string;
	sequences: string;
	consumers: string;
	crewPolicy: string;
	/** True when the store is not in the shared definition: something other than Kubemoot made it. */
	unknown: boolean;
}

const KIND_LABELS = { stream: 'Stream', kv: 'KV bucket', object: 'Object store' } as const;

/** One store as the table shows it. */
export function storeRow(s: StoreStatus): StoreRow {
	return { ...definitionColumns(s), ...usageColumns(s) };
}

/** The columns that come from the shared definition. */
function definitionColumns(s: StoreStatus) {
	const d = s.definition;
	return {
		name: s.name,
		kind: KIND_LABELS[s.kind],
		holds: d?.holds ?? 'Not created by Kubemoot',
		owner: d ? OWNER_LABELS[d.owner] : 'Unknown',
		peoplesData: d ? PEOPLES_DATA_LABELS[d.peoplesData] : 'Unknown',
		holdsPeoplesData: d ? d.peoplesData !== 'no' : false,
		crewPolicy: d?.crewPolicy ?? '',
		unknown: d === null
	};
}

function historyText(s: StoreStatus): string {
	if (s.kind !== 'kv') return '';
	const depth = String(s.limits.history ?? 1);
	const fill = historyFill(s);
	return fill === null ? depth : depth + ' (' + formatPercent(fill) + ' filled)';
}

function sequencesText(s: StoreStatus): string {
	if (s.share === 'namespaces') return 'not shown';
	return s.usage.messages === 0 ? 'empty' : s.usage.firstSeq + ' to ' + s.usage.lastSeq;
}

const withCommas = (n: number) => n.toLocaleString('en-US');

/** The columns that come from what NATS reports. */
function usageColumns(s: StoreStatus) {
	return {
		messages: withCommas(s.usage.messages),
		bytes: formatBytes(s.usage.bytes),
		bytesShare: s.share === 'namespaces' ? '' : formatPercent(fraction(s.usage.bytes, s.limits.maxBytes)),
		maxAge: formatAge(s.limits.maxAgeSeconds),
		maxBytes: formatLimit(s.limits.maxBytes, formatBytes),
		maxMessages: formatLimit(s.limits.maxMessages, withCommas),
		history: historyText(s),
		sequences: sequencesText(s),
		consumers: s.share === 'namespaces' ? '' : String(s.usage.consumers)
	};
}

/** A crew's last retention cleanup in words. */
export function retentionLine(r: CrewRetentionStatus | null | undefined): string {
	if (!r?.lastRunTime) return 'No cleanup has run yet';
	const removed = r.removed ?? {};
	const parts = [
		`${removed.conversations ?? 0} conversations`,
		`${removed.memory ?? 0} facts`,
		`${removed.other ?? 0} other`
	];
	const base = `${r.lastRunTime}: removed ${parts.join(', ')}`;
	return r.error ? `${base}; did not finish: ${r.error}` : base;
}
