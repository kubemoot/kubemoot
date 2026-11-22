// The time filter of the Conversations and Moot lists: a range chosen from a short list, or a
// custom start and end with a time of day. Pure functions of the clock, so a test can say
// which instants each range includes.

export const RANGES = ['any', '10m', '1h', '6h', 'today', '24h', '3d', 'week', 'month', '3mo', 'custom'] as const;
export type RangeKey = (typeof RANGES)[number];

/** What a list shows until a person chooses another range. */
export const DEFAULT_RANGE: RangeKey = '24h';

const LABELS: Record<RangeKey, string> = {
	any: 'Anytime',
	'10m': 'Last 10 minutes',
	'1h': 'Last hour',
	'6h': 'Last 6 hours',
	today: 'Today',
	'24h': 'Last 24 hours',
	'3d': 'Last 3 days',
	week: 'This week',
	month: 'This month',
	'3mo': 'Last 3 months',
	custom: 'Custom...'
};

export const rangeLabel = (key: RangeKey): string => LABELS[key];

export const isRangeKey = (value: string | null): value is RangeKey => RANGES.some((r) => r === value);

/** A local date and time as a datetime-local input holds it: YYYY-MM-DDTHH:mm. */
export const LOCAL_DATETIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/;
const LOCAL_DATE = /^\d{4}-\d{2}-\d{2}$/;

/** A valid local date-time, or null. A bare date is read as the start (or, for an end, the last minute) of that day. */
export function parseLocal(value: string | null, endOfDay = false): string | null {
	if (value === null) return null;
	const time = endOfDay ? '23:59' : '00:00';
	const full = LOCAL_DATE.test(value) ? `${value}T${time}` : value;
	return LOCAL_DATETIME.test(full) && !Number.isNaN(new Date(full).getTime()) ? full : null;
}

const MS = { minute: 60_000, hour: 3_600_000, day: 86_400_000 } as const;
const SINCE_MS: Partial<Record<RangeKey, number>> = {
	'10m': 10 * MS.minute,
	'1h': MS.hour,
	'6h': 6 * MS.hour,
	'24h': MS.day,
	'3d': 3 * MS.day
};

function startOfDay(now: Date): Date {
	return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}

/** Three calendar months before now, with the day kept inside the shorter month (31 May goes to 28 Feb, not 3 Mar). */
function threeMonthsBack(now: Date): number {
	const lastDay = new Date(now.getFullYear(), now.getMonth() - 2, 0).getDate();
	return new Date(now.getFullYear(), now.getMonth() - 3, Math.min(now.getDate(), lastDay), now.getHours(), now.getMinutes()).getTime();
}

/** The first instant a calendar range includes: today, this week (from Monday) or this month. */
function calendarStart(key: RangeKey, now: Date): number | null {
	const day = startOfDay(now);
	if (key === 'today') return day.getTime();
	if (key === 'week') return day.getTime() - ((day.getDay() + 6) % 7) * MS.day;
	if (key === 'month') return new Date(now.getFullYear(), now.getMonth(), 1).getTime();
	if (key === '3mo') return threeMonthsBack(now);
	return null;
}

/** The first instant a range includes, or null when it has no start. */
export function rangeStart(key: RangeKey, now: Date): number | null {
	const back = SINCE_MS[key];
	return back === undefined ? calendarStart(key, now) : now.getTime() - back;
}

/** Whether an instant falls in the range; a custom range includes both of its minutes. */
export function inTimeRange(at: string, range: { key: RangeKey; from: string | null; to: string | null }, now: Date): boolean {
	if (range.key === 'any') return true;
	const t = new Date(at).getTime();
	if (Number.isNaN(t)) return false;
	if (range.key === 'custom') {
		const from = range.from ? new Date(range.from).getTime() : null;
		const to = range.to ? new Date(range.to).getTime() + MS.minute : null;
		return (from === null || t >= from) && (to === null || t < to);
	}
	return t >= (rangeStart(range.key, now) ?? Number.NEGATIVE_INFINITY);
}

const SHOWN = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });

function shown(local: string): string {
	return SHOWN.format(new Date(local));
}

/** The words for a custom range: "Nov 1, 09:00 to Nov 21, 17:30", or one end alone. */
export function customLabel(from: string | null, to: string | null): string {
	if (from && to) return `${shown(from)} to ${shown(to)}`;
	if (from) return `From ${shown(from)}`;
	if (to) return `Until ${shown(to)}`;
	return 'Custom...';
}
