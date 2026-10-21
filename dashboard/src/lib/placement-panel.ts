// What the Placement panel of the Discussions view shows: one row per model call the
// scheduler placed, with where it ran, which policy placed it, what the placement
// predicted and what the call measured. The numbers come from the discussion receipt
// on the thread_close message (metadata.receipt.calls). A field the receipt does not
// carry stays empty; nothing is shown as zero in its place.

import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import type { PlacementCall, PlacementOption, PlacementPrediction } from '#lib/types/bus.js';
import { formatDuration } from '#lib/duration-format.js';

export type PlacementPolicy = PlacementCall['policy'];

export const PLACEMENT_POLICIES: readonly PlacementPolicy[] = ['learned', 'algorithm', 'shadow'];

export interface PlacementSummary {
	calls: number;
	/** How many calls each policy placed. */
	byPolicy: Record<PlacementPolicy, number>;
	/** Calls that carry both a prediction and a measured total. */
	measured: number;
	/** Mean absolute error of the predicted total over the measured calls. */
	meanAbsErrorMs?: number;
}

export interface PlacementPartsView {
	wait: string;
	load: string;
	prompt: string;
	generate: string;
}

export interface PlacementAlternativeView {
	place: string;
	total: string;
	parts: PlacementPartsView | null;
	warm: boolean;
	startsNow: boolean;
}

export interface PlacementRowView {
	agent: string;
	phase: string;
	place: string;
	policy: string;
	coldStart: boolean;
	reason: string;
	hasPrediction: boolean;
	predictedTotal: string;
	actualTotal: string;
	/** Signed error of the total, such as "+500ms (+14.7%)"; empty when it cannot be worked out. */
	totalError: string;
	predictedFirstToken: string;
	actualFirstToken: string;
	firstTokenError: string;
	failed: boolean;
	alternatives: PlacementAlternativeView[];
}

function isCall(value: unknown): value is PlacementCall {
	return (
		typeof value === 'object' && value !== null && typeof (value as PlacementCall).model === 'string'
	);
}

/** The placement calls of a thread: those on its latest thread_close receipt, or none. */
export function placementCallsOf(messages: readonly DiscussionMessage[]): PlacementCall[] {
	for (let i = messages.length - 1; i >= 0; i--) {
		if (messages[i].messageType !== 'thread_close') continue;
		const calls: unknown = messages[i].metadata?.receipt?.calls;
		return Array.isArray(calls) ? calls.filter(isCall) : [];
	}
	return [];
}

function finite(value: number | undefined): value is number {
	return typeof value === 'number' && Number.isFinite(value);
}

function signedDuration(ms: number): string {
	const magnitude = formatDuration(Math.abs(ms));
	if (magnitude === '') return '';
	return `${ms < 0 ? '-' : '+'}${magnitude}`;
}

/** Relative error as a signed percentage; empty when the base is zero or unknown. */
function signedPercent(error: number, base: number | undefined): string {
	if (!finite(base) || base <= 0) return '';
	const pct = (error / base) * 100;
	return `${pct < 0 ? '-' : '+'}${Math.abs(pct).toFixed(1)}%`;
}

/**
 * "+500ms (+14.7%)" from a signed error (measured minus predicted, so positive means
 * slower than predicted) and the measured value the percentage is relative to.
 */
export function formatSignedError(
	errorMs: number | undefined,
	actualMs: number | undefined
): string {
	if (!finite(errorMs)) return '';
	const abs = signedDuration(errorMs);
	if (abs === '') return '';
	const pct = signedPercent(errorMs, actualMs);
	return pct === '' ? abs : `${abs} (${pct})`;
}

/**
 * The time to the first token a placement predicts: the figure the receipt carries, or
 * for a receipt written before it existed, waiting plus loading plus reading the prompt.
 */
export function predictedFirstTokenMs(p: PlacementPrediction | undefined): number | undefined {
	if (!p) return undefined;
	if (finite(p.firstTokenMs)) return p.firstTokenMs;
	const sum = p.waitMs + p.loadMs + p.promptMs;
	return Number.isFinite(sum) ? sum : undefined;
}

/** The signed error of the total in ms (measured minus predicted), from the receipt or worked out. */
function totalErrorMs(call: PlacementCall): number | undefined {
	if (finite(call.error?.totalSignedMs)) return call.error.totalSignedMs;
	if (call.predicted && call.actual && finite(call.actual.totalMs)) {
		return call.actual.totalMs - call.predicted.totalMs;
	}
	return undefined;
}

function firstTokenErrorMs(call: PlacementCall): number | undefined {
	const predicted = predictedFirstTokenMs(call.predicted);
	const actual = call.actual?.firstTokenMs;
	if (predicted === undefined || !finite(actual)) return undefined;
	return actual - predicted;
}

/** Counts the calls per policy and the mean absolute error of the predicted totals. */
export function summarizePlacement(calls: readonly PlacementCall[]): PlacementSummary {
	const byPolicy: Record<PlacementPolicy, number> = { learned: 0, algorithm: 0, shadow: 0 };
	const errors: number[] = [];
	for (const call of calls) {
		if (call.policy in byPolicy) byPolicy[call.policy]++;
		const err = totalErrorMs(call);
		if (err !== undefined) errors.push(Math.abs(err));
	}
	const mean = errors.length > 0 ? errors.reduce((a, b) => a + b, 0) / errors.length : undefined;
	return { calls: calls.length, byPolicy, measured: errors.length, meanAbsErrorMs: mean };
}

/** The one-line summary above the rows; empty when there are no calls. */
export function placementSummaryLine(summary: PlacementSummary): string {
	if (summary.calls === 0) return '';
	const noun = summary.calls === 1 ? 'call' : 'calls';
	const policies = PLACEMENT_POLICIES.filter((p) => summary.byPolicy[p] > 0)
		.map((p) => `${summary.byPolicy[p]} ${p}`)
		.join(', ');
	const head =
		policies === '' ? `${summary.calls} ${noun}.` : `${summary.calls} ${noun}: ${policies}.`;
	if (summary.meanAbsErrorMs === undefined) return `${head} No predictions to compare.`;
	const over = summary.measured === 1 ? '1 call' : `${summary.measured} calls`;
	return `${head} Mean absolute error ${formatDuration(summary.meanAbsErrorMs)} over ${over}.`;
}

function partsOf(p: PlacementPrediction | undefined): PlacementPartsView | null {
	if (!p) return null;
	return {
		wait: formatDuration(p.waitMs),
		load: formatDuration(p.loadMs),
		prompt: formatDuration(p.promptMs),
		generate: formatDuration(p.generateMs)
	};
}

function alternativeView(option: PlacementOption, fallbackModel: string): PlacementAlternativeView {
	return {
		place: `${option.model ?? fallbackModel}@${option.server}`,
		total: option.predicted ? formatDuration(option.predicted.totalMs) : 'no prediction',
		parts: partsOf(option.predicted),
		warm: option.warm === true,
		startsNow: option.startsNow === true
	};
}

function timesOf(call: PlacementCall) {
	const predicted = call.predicted;
	const actual = call.actual;
	const predictedFirst = predictedFirstTokenMs(predicted);
	return {
		predictedTotal: predicted ? formatDuration(predicted.totalMs) : '',
		actualTotal: actual ? formatDuration(actual.totalMs) : '',
		totalError: formatSignedError(totalErrorMs(call), actual?.totalMs),
		predictedFirstToken: predictedFirst === undefined ? '' : formatDuration(predictedFirst),
		actualFirstToken: formatDuration(actual?.firstTokenMs),
		firstTokenError: formatSignedError(firstTokenErrorMs(call), actual?.firstTokenMs)
	};
}

/** The display model of one call. */
export function placementRow(call: PlacementCall): PlacementRowView {
	return {
		agent: call.agent ?? '',
		phase: call.phase ?? '',
		place: `${call.model}@${call.server}`,
		policy: call.policy,
		coldStart: call.coldStart === true,
		reason: call.reason ?? '',
		hasPrediction: call.predicted !== undefined,
		...timesOf(call),
		failed: call.actual !== undefined && call.actual.ok === false,
		alternatives: (call.alternatives ?? []).map((o) => alternativeView(o, call.model))
	};
}
