import type { Requester } from '#lib/types/bus.js';

/** True when the browser knows this name as an IANA time zone. */
export function isTimeZone(name: unknown): name is string {
	if (typeof name !== 'string' || name.trim() === '') return false;
	try {
		new Intl.DateTimeFormat('en-US', { timeZone: name });
		return true;
	} catch {
		return false;
	}
}

function pad(value: number): string {
	return String(value).padStart(2, '0');
}

/** The date's local wall-clock time as ISO 8601 without an offset, such as 2026-10-24T09:15:00. */
export function localDateTime(now: Date): string {
	const date = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
	return `${date}T${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}`;
}

/**
 * Where and when the person asking is: their browser's IANA time zone and local date-time.
 * Tools that need today's date read it from the question, not from a server's clock.
 * Returns undefined when the browser reports no usable time zone, so the question carries nothing.
 */
export function currentRequester(
	now: Date = new Date(),
	zone: unknown = Intl.DateTimeFormat().resolvedOptions().timeZone
): Requester | undefined {
	if (!isTimeZone(zone) || Number.isNaN(now.getTime())) return undefined;
	return { timeZone: zone.trim(), localDateTime: localDateTime(now) };
}
