// The one duration format of the dashboard's run and discussion views: "850ms",
// "12.3s", "1m12s", "1h02m". Callers choose what to show for an unknown duration.

/** A duration as text; empty when it is unknown, not finite, or negative. */
export function formatDuration(ms: number | undefined): string {
	if (ms === undefined || !Number.isFinite(ms) || ms < 0) return '';
	if (ms < 1000) return `${Math.round(ms)}ms`;
	if (ms < 60_000) return `${(Math.floor(ms / 100) / 10).toFixed(1)}s`;
	const minutes = Math.floor(ms / 60_000);
	const seconds = Math.floor((ms % 60_000) / 1000);
	if (minutes < 60) return `${minutes}m${String(seconds).padStart(2, '0')}s`;
	return `${Math.floor(minutes / 60)}h${String(minutes % 60).padStart(2, '0')}m`;
}

/** Time since a start, such as "+1m12s"; "+0s" within the first second; empty when unknown. */
export function formatElapsed(ms: number | undefined): string {
	if (ms === undefined || !Number.isFinite(ms) || ms < 0) return '';
	return ms < 1000 ? '+0s' : `+${formatDuration(ms)}`;
}
