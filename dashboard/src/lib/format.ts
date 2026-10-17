const MIB = 1024 * 1024;

/** Memory given in MiB: "800 MiB", or "9.3 GiB" from 1024 MiB; "-" when unknown or not above zero. */
export function formatMiB(mib: number | undefined | null): string {
	if (typeof mib !== 'number' || !Number.isFinite(mib) || mib <= 0) return '-';
	const whole = Math.round(mib);
	if (whole >= 1024) return `${(mib / 1024).toFixed(1)} GiB`;
	return `${whole} MiB`;
}

/** Memory given in bytes, shown as formatMiB shows MiB. */
export function formatBytes(bytes: number | undefined | null): string {
	if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes <= 0) return '-';
	return formatMiB(bytes / MIB);
}
