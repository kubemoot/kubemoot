import type { Usage } from './node-usage.js';

/** At or above this share of what is available, CPU or memory is raised as high use. */
export const HIGH_USE_FRACTION = 0.9;
/** At or above this share a usage bar is drawn as critical. */
export const CRITICAL_USE_FRACTION = 0.97;

/** One small bar: a measurement against what is available. */
export interface TileBar {
	label: string;
	/** The measurement in words: "6.1 of 16 GiB". */
	text: string;
	/** Share in use, 0 to 1. */
	fraction: number;
	level: 'normal' | 'warning' | 'critical';
}

export function levelOf(fraction: number): TileBar['level'] {
	if (fraction >= CRITICAL_USE_FRACTION) return 'critical';
	return fraction >= HIGH_USE_FRACTION ? 'warning' : 'normal';
}

/** A bar for a use; the share is kept between 0 and 1. */
export function bar(label: string, text: string, fraction: number): TileBar {
	const f = Number.isFinite(fraction) ? Math.min(1, Math.max(0, fraction)) : 0;
	return { label, text, fraction: f, level: levelOf(f) };
}

/** A bar for a measured use: "2.5 of 67.4 cores". */
export function usageBar(label: string, use: Usage, format: (n: number) => string, unit = ''): TileBar {
	return bar(label, `${format(use.used)} of ${format(use.allocatable)}${unit}`, use.fraction);
}
