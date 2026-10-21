import { formatMiB } from '#lib/format.js';
import type { BadgeStatus } from '#lib/status.js';
import type { ModelCalibration, ServerCalibration } from '#lib/types/kubemoot.js';

/** One model's calibration as the server page shows it. */
export interface CalibrationRow {
	model: string;
	/** Whether the model was measured, skipped as not applicable, or could not be measured. */
	outcome: 'measured' | 'skipped' | 'failed';
	fits: boolean;
	/** Load time, such as "2.9 s". */
	load: string;
	/** Generation speed of one request, such as "235 tok/s". */
	generate: string;
	/** Prompt processing speed. */
	prompt: string;
	/** Total generation speed with requests together, such as "2 x 241 tok/s". */
	parallel: string;
	/** Memory it took once loaded. */
	resident: string;
	/** Why it does not fit or could not be measured; empty when calibration gave none. */
	reason: string;
}

const NONE = '-';

/** The outcome a model's status stands for; an empty or unknown status is a measurement. */
function outcomeOf(status: unknown): CalibrationRow['outcome'] {
	if (status === 'Skipped') return 'skipped';
	if (status === 'Failed') return 'failed';
	return 'measured';
}

/** The reason to show: the one calibration gave, else what the outcome implies. */
function reasonOf(m: ModelCalibration, outcome: CalibrationRow['outcome']): string {
	if (m.reason) return m.reason;
	if (outcome === 'failed') return 'Could not be measured';
	return outcome === 'measured' && m.fits !== true ? 'Does not fit' : '';
}

function positive(n: unknown): n is number {
	return typeof n === 'number' && Number.isFinite(n) && n > 0;
}

/** A speed in tokens per second, with thousands grouped in the viewer's locale. */
export function tokensPerSecond(n: unknown): string {
	return positive(n) ? `${Math.round(n).toLocaleString()} tok/s` : NONE;
}

/** A duration in milliseconds as seconds with one decimal, or milliseconds under one second. */
export function loadTime(ms: unknown): string {
	if (!positive(ms)) return NONE;
	const whole = Math.round(ms);
	return whole < 1000 ? `${whole} ms` : `${(ms / 1000).toFixed(1)} s`;
}

/** Memory in MiB. */
export function memory(mib: unknown): string {
	return positive(mib) ? formatMiB(mib) : NONE;
}

function parallel(m: ModelCalibration): string {
	if (!positive(m.parallelRequests) || !positive(m.parallelGenerateTokensPerSecond)) return NONE;
	return `${m.parallelRequests} x ${tokensPerSecond(m.parallelGenerateTokensPerSecond)}`;
}

/** A skipped or failed model has no figures; they stay empty however the entry was filled. */
function rowOf(m: ModelCalibration): CalibrationRow {
	const outcome = outcomeOf(m.status);
	const measured = outcome === 'measured';
	return {
		model: m.model,
		outcome,
		fits: m.fits === true,
		load: measured ? loadTime(m.loadMs) : NONE,
		generate: measured ? tokensPerSecond(m.generateTokensPerSecond) : NONE,
		prompt: measured ? tokensPerSecond(m.promptTokensPerSecond) : NONE,
		parallel: measured ? parallel(m) : NONE,
		resident: measured ? memory(m.residentMiB) : NONE,
		reason: reasonOf(m, outcome)
	};
}

/** The rows for a calibration, sorted by model name; none when there is no calibration. */
export function calibrationRows(cal: ServerCalibration | undefined | null): CalibrationRow[] {
	const models = Array.isArray(cal?.models) ? cal.models : [];
	return models
		.filter((m) => m && typeof m.model === 'string' && m.model !== '')
		.map((m) => rowOf(m))
		.sort((a, b) => a.model.localeCompare(b.model));
}

/** The badge state for a calibration phase. */
export function calibrationBadge(phase: string | undefined): BadgeStatus {
	switch (phase) {
		case 'Calibrated':
			return 'success';
		case 'Calibrating':
		case 'Waiting':
			return 'pending';
		case 'Failed':
			return 'error';
		default:
			return 'unknown';
	}
}

/** Whether a status carries any calibration to show: a phase or a measured model. */
export function hasCalibration(cal: ServerCalibration | undefined | null): cal is ServerCalibration {
	return Boolean(cal?.phase) || calibrationRows(cal).length > 0;
}
