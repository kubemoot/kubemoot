// Formatting for the Fitness page: durations, sizes, times, and the markdown a
// suite row copies to the clipboard.

import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';
import { NO_VALUE } from '#lib/resource-status.js';

export interface JudgeProgress {
	complete: boolean;
	judged: number;
}

/** Elapsed run time of a suite: start to completion, or to now while it runs. */
export function suiteDurMs(s?: CrewFitnessSuite['status'], now = Date.now()): number | undefined {
	if (!s?.startedAt) return undefined;
	const end = s.completedAt ? new Date(s.completedAt).getTime() : now;
	return end - new Date(s.startedAt).getTime();
}

export function fmtDur(ms?: number): string {
	if (!ms) return NO_VALUE;
	if (ms < 1000) return `${ms}ms`;
	if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
	return `${Math.floor(ms / 60_000)}m ${Math.floor((ms % 60_000) / 1000)}s`;
}

export function fmtDateTime(iso?: string): string {
	if (!iso) return NO_VALUE;
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? NO_VALUE : d.toLocaleString();
}

/** Tooltip for a Duration cell: the start and end behind the relative duration. */
export function durTitle(startedAt?: string, completedAt?: string): string {
	if (!startedAt) return 'No start time recorded';
	const ended = completedAt ? fmtDateTime(completedAt) : 'running';
	return `Started: ${fmtDateTime(startedAt)}\nEnded: ${ended}`;
}

export function formatBytes(n?: number): string {
	if (!n) return NO_VALUE;
	if (n < 1024) return `${n} B`;
	if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
	return `${(n / 1024 / 1024).toFixed(2)} MB`;
}

function judgeLine(judge: JudgeProgress | undefined, scenarioCount: number): string {
	if (!judge) return '';
	const state = judge.complete ? 'complete' : `judging ${judge.judged}/${scenarioCount}`;
	return `- Judge: ${state}`;
}

function artifactLine(ref: NonNullable<CrewFitnessSuite['status']>['artifactRef']): string {
	if (!ref?.objectKey) return '';
	return `- Artifact: ${ref.objectKey} (${formatBytes(ref.sizeBytes)})`;
}

/** A line that is present only when its value is set. */
function optionalLine(label: string, value: string | undefined): string {
	return value ? `- ${label}: ${value}` : '';
}

function resultLines(status: CrewFitnessSuite['status']): string[] {
	const s: NonNullable<CrewFitnessSuite['status']> = status ?? {};
	return [
		`- Phase: ${s.phase ?? 'Unknown'}`,
		`- Iterations: ${s.iterationsCompleted ?? 0} / ${s.iterationsTotal ?? 0}`,
		`- Results: ${s.passed ?? 0} passed / ${s.failed ?? 0} failed / ${s.errored ?? 0} errored`
	];
}

/** A suite's run context as markdown for a report or ticket; empty lines are left out. */
export function suiteMarkdown(
	suite: CrewFitnessSuite,
	judge: JudgeProgress | undefined,
	now = Date.now()
): string {
	const s = suite.status;
	const ns = suite.metadata.namespace ?? '';
	const scenarios = suite.spec.scripts?.map((x) => x.testRef) ?? [];
	return [
		`# Fitness Suite: ${suite.metadata.name ?? ''}`,
		`- Crew: ${suite.spec.crewRef}`,
		`- Namespace: ${ns}`,
		optionalLine('Description', suite.spec.description),
		...resultLines(s),
		`- Duration: ${fmtDur(suiteDurMs(s, now)).replace(NO_VALUE, '-')}`,
		`- Scenarios (${scenarios.length}): ${scenarios.join(', ')}`,
		judgeLine(judge, scenarios.length),
		optionalLine('Run ID', s?.runId),
		optionalLine('Created', suite.metadata.creationTimestamp),
		artifactLine(s?.artifactRef)
	]
		.filter(Boolean)
		.join('\n');
}
