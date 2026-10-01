// Pure helpers for the per-iteration rows of a CrewFitnessSuite run, read from the
// s{idx}-i{iter}.json transcript blobs in the NATS object store.

const ITERATION_KEY_RE = /^s(\d+)-i(\d+)\.json$/;

export interface TranscriptSummary {
	assertions?: { passed: boolean }[];
	durationMs?: number;
}

export interface IterationResult {
	status: 'Passed' | 'Failed' | 'Unknown';
	assertionsPassed: number;
	assertionsTotal: number;
	durationMs: number;
}

/** True for a per-iteration transcript name; false for sidecars such as judge-v1.json. */
export function isIterationKey(tail: string): boolean {
	return ITERATION_KEY_RE.test(tail);
}

/** The script index and iteration number in a transcript name, -1 for each when it does not parse. */
export function parseIterationKey(tail: string): { scriptIdx: number; iter: number } {
	const m = ITERATION_KEY_RE.exec(tail);
	if (!m) return { scriptIdx: -1, iter: -1 };
	return { scriptIdx: Number.parseInt(m[1], 10), iter: Number.parseInt(m[2], 10) };
}

/** The scenario label of a script: its testRef, else "script <idx>". */
export function scenarioLabel(scripts: { testRef?: string }[], scriptIdx: number): string {
	const testRef = scriptIdx >= 0 ? scripts[scriptIdx]?.testRef : undefined;
	return testRef || `script ${scriptIdx}`;
}

/** Passed when every assertion passed, Failed when any failed, Unknown without assertions. */
export function iterationStatus(passed: number, total: number): IterationResult['status'] {
	if (total === 0) return 'Unknown';
	return passed === total ? 'Passed' : 'Failed';
}

/** The row figures for one transcript; a missing transcript gives an Unknown, empty row. */
export function summarizeTranscript(t: TranscriptSummary | null): IterationResult {
	if (!t) return { status: 'Unknown', assertionsPassed: 0, assertionsTotal: 0, durationMs: 0 };
	const assertions = t.assertions ?? [];
	const assertionsPassed = assertions.filter((x) => x.passed).length;
	return {
		status: iterationStatus(assertionsPassed, assertions.length),
		assertionsPassed,
		assertionsTotal: assertions.length,
		durationMs: t.durationMs ?? 0
	};
}
