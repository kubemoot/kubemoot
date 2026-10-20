import { formatMiB } from '#lib/format.js';
import type { BadgeStatus } from '#lib/status.js';
import type { ModelCalibration, ServerCalibration } from '#lib/types/kubemoot.js';

/** One model's calibration as the server page shows it. */
export interface CalibrationRow {
	model: string;
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

/** The rows for a calibration, sorted by model name; none when there is no calibration. */
export function calibrationRows(cal: ServerCalibration | undefined | null): CalibrationRow[] {
	const models = Array.isArray(cal?.models) ? cal.models : [];
	return models
		.filter((m) => m && typeof m.model === 'string' && m.model !== '')
		.map((m) => ({
			model: m.model,
			fits: m.fits === true,
			load: loadTime(m.loadMs),
			generate: tokensPerSecond(m.generateTokensPerSecond),
			prompt: tokensPerSecond(m.promptTokensPerSecond),
			parallel: parallel(m),
			resident: memory(m.residentMiB),
			reason: m.reason || (m.fits === true ? '' : 'Does not fit')
		}))
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
