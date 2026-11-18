// What a suite run ran on, and whether two runs are comparable. The operator
// hashes the comparable parts of a run's realm into status.realm.digests, so
// this compares those hashes and names the parts that differ; it never
// recomputes what "comparable" means.

import type { CrewFitnessSuite, FitnessRealm, RealmDigests } from '#lib/types/kubemoot.js';

type SuiteLike = Pick<CrewFitnessSuite, 'metadata' | 'spec' | 'status'>;

const PARTS: ReadonlyArray<keyof RealmDigests> = ['servers', 'models', 'versions', 'resources'];

/** The parts of two realms that differ, in a fixed order; ['unknown'] when either run recorded none. */
export function differingParts(a: RealmDigests | undefined, b: RealmDigests | undefined): string[] {
	if (!a || !b) return ['unknown'];
	return PARTS.filter((p) => a[p] !== b[p]);
}

export interface RealmView {
	/** The run's realm as a short label, or "not recorded". */
	label: string;
	/** Things to warn about, each one line. */
	warnings: string[];
}

/** The latest run of the same crew that started before this one. */
export function previousRun(suite: SuiteLike, all: SuiteLike[]): SuiteLike | undefined {
	const started = suite.status?.startedAt ? Date.parse(suite.status.startedAt) : NaN;
	if (Number.isNaN(started)) return undefined;
	const earlier = all.filter(
		(s) =>
			s.metadata.namespace === suite.metadata.namespace &&
			s.spec.crewRef === suite.spec.crewRef &&
			s.metadata.name !== suite.metadata.name &&
			s.status?.startedAt &&
			Date.parse(s.status.startedAt) < started
	);
	earlier.sort((x, y) => Date.parse(y.status!.startedAt!) - Date.parse(x.status!.startedAt!));
	return earlier[0];
}

/** The warning when this run's realm differs from the previous run of the same crew. */
function comparisonWarning(suite: SuiteLike, all: SuiteLike[]): string[] {
	const prev = previousRun(suite, all);
	if (!prev) return [];
	const parts = differingParts(prev.status?.realm?.digests, suite.status?.realm?.digests);
	return parts.length > 0
		? [`realm differs from ${prev.metadata.name} (${parts.join(', ')}): do not compare the scores`]
		: [];
}

/** Warnings from what the run itself recorded: drift, other suites, resource notes. */
function recordedWarnings(realm: FitnessRealm): string[] {
	const counts: Array<[number, string]> = [
		[count(realm.drift), 'change(s) between start and end'],
		[count(realm.start?.concurrentSuites), 'other suite(s) ran at the same time'],
		[count(realm.start?.resourceNotes), 'agent resource note(s)']
	];
	return counts.filter(([n]) => n > 0).map(([n, text]) => `${n} ${text}`);
}

function count(list: string[] | undefined): number {
	return list?.length ?? 0;
}

/** The label and warnings for one run, compared with the previous run of the same crew. */
export function realmView(suite: SuiteLike, all: SuiteLike[]): RealmView {
	const realm = suite.status?.realm;
	if (!realm?.digests) {
		return { label: 'not recorded', warnings: ['no realm recorded: cannot be compared to another run'] };
	}
	const d = realm.digests;
	const label = [d.servers, d.models, d.versions, d.resources].map((h) => h.slice(0, 4)).join('.');
	return { label, warnings: [...comparisonWarning(suite, all), ...recordedWarnings(realm)] };
}
