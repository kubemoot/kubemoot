// Text the discussion span graph prints: durations, the label beside a bar, and the
// width of the name column. Nothing here ever prints a non-finite number.

/** Average width of one character of the graph's 12px monospace label font. */
const CHAR_PX = 7.4;
const LABEL_PADDING_PX = 48;
const MIN_LABEL_PX = 160;
const MAX_LABEL_SHARE = 0.45;

/** A duration as ms under one second, else seconds; a missing or non-finite value is "-". */
export function formatSpanDuration(ms: number | null | undefined): string {
	if (ms == null || !Number.isFinite(ms) || ms < 0) return '-';
	if (ms < 1000) return `${Math.round(ms)}ms`;
	return `${(ms / 1000).toFixed(1)}s`;
}

/** The width of a server badge for its text, at the graph's 9px monospace font. */
export function badgeWidth(text: string): number {
	return text.length * 5.6 + 10;
}

/** The width of the name column: wide enough for the longest name and badge, never over 45% of the graph. */
export function labelColumnWidth(names: readonly string[], badgePx: number, containerWidth: number): number {
	const longest = names.reduce((n, name) => Math.max(n, name.length), 0);
	const wanted = longest * CHAR_PX + badgePx + LABEL_PADDING_PX;
	return Math.max(MIN_LABEL_PX, Math.min(wanted, containerWidth * MAX_LABEL_SHARE));
}

/** The name shortened with an ellipsis (at its start kept, tail cut) when it cannot fit `maxPx`. */
export function fitLabel(name: string, maxPx: number): string {
	const maxChars = Math.max(Math.floor(maxPx / CHAR_PX), 4);
	return name.length <= maxChars ? name : `${name.slice(0, maxChars - 1)}...`;
}

/** The text beside a timed bar: total, then inference and time to first token when known. */
export function spanTimingLabel(totalMs: number, inferenceMs: number | null | undefined, firstTokenMs?: number): string {
	const total = formatSpanDuration(totalMs);
	if (inferenceMs == null || !Number.isFinite(inferenceMs) || inferenceMs <= 0) return total;
	const parts = [`${formatSpanDuration(inferenceMs)} inference`];
	if (firstTokenMs != null && Number.isFinite(firstTokenMs) && firstTokenMs > 0) {
		parts.push(`${formatSpanDuration(firstTokenMs)} to first token`);
	}
	return `${total} (${parts.join(', ')})`;
}

/** Approximate pixel width of a label at the graph's 10px monospace font. */
export function labelPx(text: string): number {
	return text.length * 6.2;
}
