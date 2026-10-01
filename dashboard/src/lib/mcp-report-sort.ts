import type { MCPServerReport } from '$types/kubemoot.js';

type ReportComparator = (a: MCPServerReport, b: MCPServerReport) => number;

const byServerName: ReportComparator = (a, b) =>
	(a.spec?.serverName || '').localeCompare(b.spec?.serverName || '');

const successRate = (r: MCPServerReport) => Number.parseFloat(r.status?.successRate || '0');

// Newest test first, highest success rate first; any other key sorts by server name.
const COMPARATORS: Record<string, ReportComparator> = {
	lastTested: (a, b) => (b.status?.lastTested || '').localeCompare(a.status?.lastTested || ''),
	successRate: (a, b) => successRate(b) - successRate(a)
};

/** The comparator for the report list's sort key. */
export function reportComparator(sortBy: string): ReportComparator {
	return Object.hasOwn(COMPARATORS, sortBy) ? COMPARATORS[sortBy] : byServerName;
}
