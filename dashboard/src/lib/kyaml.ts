// KYAML: the Kubernetes dialect of YAML that `kubectl get -o kyaml` writes (Kubernetes 1.34+,
// KEP-5295). It is a strict subset of YAML that does not depend on whitespace: every mapping is
// braces, every list is brackets, every value string is double-quoted, and a key is bare only
// when it cannot be read as another type. No maintained JavaScript encoder exists, so this
// follows the reference encoder, sigs.k8s.io/yaml/kyaml, for the part a JSON document uses (no
// comments, no aliases): two-space indent, a trailing comma after every entry, lists of
// mappings or lists cuddled (`[{` ... `}],`), `{}` and `[]` for empty ones, map keys sorted
// (kubectl reads unstructured objects, which are sorted), and a multi-line string written with
// YAML line folding. The output is meant to match kubectl's byte for byte.

const INDENT = '  ';
const FOLD = '\\\n';
const HEADER = '---\n';

const pad = (level: number): string => INDENT.repeat(level);

// ---- type-ambiguous strings: what a YAML 1.1 parser would not read as a string ----

const SEXAGESIMAL = /^[+-]?[1-9][\d_]*(:[0-5]?\d)+(\.[\d_]*)?$/;
const DECIMAL_INT = /^[+-]?(0|[1-9]\d*)$/;
const PREFIXED_INT = /^([+-]?)0([xX][0-9a-fA-F]+|[bB][01]+|[oO][0-7]+|[0-7]+)$/;
const DECIMAL_FLOAT = /^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$/;
const HEX_FLOAT = /^[+-]?0[xX]([0-9a-fA-F]+(\.[0-9a-fA-F]*)?|\.[0-9a-fA-F]+)[pP][+-]?\d+$/;
const INFINITY = /^[+-]?(inf|infinity)$/i;
const TIMESTAMP_DATE = /^(\d{4})-(\d{1,2})-(\d{1,2})(.*)$/s;
const TIMESTAMP_TIME = /^[Tt ](\d{2}):(\d{1,2}):(\d{1,2})(\.\d+)?(Z|[+-]\d{2}:\d{2})?$/;
// strconv.ParseBool
const BOOL_WORDS = new Set(['1', 't', 'T', 'TRUE', 'true', 'True', '0', 'f', 'F', 'FALSE', 'false', 'False']);
const SHORT_WORDS = new Set(['null', '~', '', 'true', 'y', 'yes', 'on', 'false', 'n', 'no', 'off', '.inf', '-.inf', '+.inf', '.nan']);

const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;

/** Go's strconv.ParseInt(s, 0, 64) succeeding: decimal, 0x, 0b, 0o or leading-zero octal, within int64. */
function parsesAsInt64(s: string): boolean {
	if (DECIMAL_INT.test(s)) return inInt64(BigInt(s));
	const m = PREFIXED_INT.exec(s);
	if (!m) return false;
	const body = m[2];
	const digits = 'xXbBoO'.includes(body[0]) ? `0${body.toLowerCase()}` : `0o${body}`;
	return inInt64(m[1] === '-' ? -BigInt(digits) : BigInt(digits));
}

function inInt64(n: bigint): boolean {
	return n >= INT64_MIN && n <= INT64_MAX;
}

/** Go's strconv.ParseFloat succeeding: a finite decimal or hex float, an infinity, or nan. */
function parsesAsFloat(s: string): boolean {
	if (DECIMAL_FLOAT.test(s)) return Number.isFinite(Number(s));
	return HEX_FLOAT.test(s) || INFINITY.test(s) || s.toLowerCase() === 'nan';
}

function validDate(year: number, month: number, day: number): boolean {
	if (month < 1 || month > 12 || day < 1) return false;
	return day <= new Date(Date.UTC(2000 + (year % 400), month, 0)).getUTCDate();
}

/** A date, or a date with a time, in the shapes the YAML 1.1 timestamp type covers. */
function looksLikeTimestamp(s: string): boolean {
	const m = TIMESTAMP_DATE.exec(s);
	if (!m || !validDate(Number(m[1]), Number(m[2]), Number(m[3]))) return false;
	if (m[4] === '') return true;
	const t = TIMESTAMP_TIME.exec(m[4]);
	if (!t || Number(t[1]) > 23 || Number(t[2]) > 59 || Number(t[3]) > 59) return false;
	// A time with a "T" needs a zone; one with a space has none.
	return m[4].startsWith(' ') ? t[5] === undefined : t[5] !== undefined;
}

/** True when an unquoted form of the string would be read as null, a boolean, a number or a timestamp. */
export function isTypeAmbiguous(s: string): boolean {
	if (BOOL_WORDS.has(s) || (s.length <= 5 && SHORT_WORDS.has(s.toLowerCase()))) return true;
	const plain = s.replaceAll('_', '');
	return parsesAsInt64(plain) || parsesAsFloat(plain) || SEXAGESIMAL.test(s) || looksLikeTimestamp(s);
}

const UNQUOTED_ANYWHERE = '_';
const UNQUOTED_INTERIOR = '-./';

/** True when a key or value cannot be written bare. */
export function needsQuotes(s: string): boolean {
	if (s === '' || isTypeAmbiguous(s)) return true;
	const chars = [...s];
	return chars.some((ch, i) => {
		if (/[\p{L}\p{N}]/u.test(ch) || UNQUOTED_ANYWHERE === ch) return false;
		return !(i > 0 && i < chars.length - 1 && UNQUOTED_INTERIOR.includes(ch));
	});
}

// ---- escaping ----

const SPACE_RANGES: readonly (readonly [number, number])[] = [
	[0x09, 0x0d], [0x20, 0x20], [0x85, 0x85], [0xa0, 0xa0], [0x1680, 0x1680], [0x2000, 0x200a],
	[0x2028, 0x2029], [0x202f, 0x202f], [0x205f, 0x205f], [0x3000, 0x3000]
];

/** Go's unicode.IsSpace. */
function isSpace(code: number): boolean {
	return SPACE_RANGES.some(([low, high]) => code >= low && code <= high);
}

/** Go's unicode.IsPrint: letters, marks, numbers, punctuation, symbols and the ASCII space. */
function isPrint(ch: string): boolean {
	return ch === ' ' || /[\p{L}\p{M}\p{N}\p{P}\p{S}]/u.test(ch);
}

const NAMED_ESCAPES: Record<number, string> = {
	0x07: String.raw`\a`, 0x08: String.raw`\b`, 0x0c: String.raw`\f`, 0x0a: String.raw`\n`, 0x0d: String.raw`\r`,
	0x09: String.raw`\t`, 0x0b: String.raw`\v`, 0x00: String.raw`\0`, 0x1b: String.raw`\e`, 0x85: String.raw`\N`,
	0xa0: String.raw`\_`, 0x2028: String.raw`\L`, 0x2029: String.raw`\P`
};

function hex(code: number, width: number): string {
	return code.toString(16).padStart(width, '0');
}

/** The escape for a character that is not printable: a named one, else \xNN, \uNNNN or \UNNNNNNNN. */
function nonPrintable(ch: string): string {
	const code = ch.codePointAt(0) ?? 0;
	const lone = code >= 0xd800 && code <= 0xdfff;
	if (NAMED_ESCAPES[code] !== undefined) return NAMED_ESCAPES[code];
	if (lone) return String.raw`\ufffd`;
	if (code < 0x20 || code === 0x7f) return String.raw`\x` + hex(code, 2);
	return code < 0x10000 ? String.raw`\u` + hex(code, 4) : String.raw`\U` + hex(code, 8);
}

/** One character of a key, which is always on one line: a tab and a line break are escaped. */
function escapeKey(ch: string): string {
	if (ch === '\n') return String.raw`\n`;
	if (ch === '\t') return String.raw`\t`;
	return escapeAlways(ch);
}

/** One character of a value on one line: a tab stays a tab. */
function escapeLine(ch: string): string {
	return ch === '\t' ? ch : escapeAlways(ch);
}

function escapeAlways(ch: string): string {
	if (ch === '"' || ch === '\\') return `\\${ch}`;
	return isPrint(ch) ? ch : nonPrintable(ch);
}

/**
 * One character of a multi-line string. After a line break the line starts with the indent, then
 * a backslash when the character is whitespace (so the fold keeps it) or one space of padding.
 */
function escapeFolded(ch: string, indent: number, afterNewline: boolean): string {
	const keepsSpace = isSpace(ch.codePointAt(0) ?? 0) && ch !== '\n';
	const marker = keepsSpace ? '\\' : ' ';
	const lead = afterNewline ? pad(indent) + marker : '';
	if (ch === '\n') return lead + String.raw`\n` + FOLD;
	if (ch === '\t') return `${lead}\t`;
	return lead + escapeAlways(ch);
}

/** A string in double quotes; a multi-line value is folded over lines indented to `indent`. */
function quote(value: string, indent: number, asKey: boolean): string {
	const multi = !asKey && value.includes('\n');
	const one = asKey ? escapeKey : escapeLine;
	let out = multi ? `"${FOLD}` : '"';
	for (const ch of value) out += multi ? escapeFolded(ch, indent, out.endsWith('\n')) : one(ch);
	if (multi) out += (out.endsWith('\n') ? '' : FOLD) + pad(indent);
	return out + '"';
}

/** A map key: bare when it is plain and unambiguous, else quoted on one line. */
function keyText(key: string): string {
	return !key.includes('\n') && !needsQuotes(key) ? key : quote(key, 0, true);
}

// ---- structure ----

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

const isContainer = (v: Json): v is Json[] | { [key: string]: Json } => typeof v === 'object' && v !== null;

/** Code point order, which is the byte order of UTF-8 that Go sorts keys by. */
function compareCodePoints(a: string, b: string): number {
	const x = [...a];
	const y = [...b];
	for (let i = 0; i < Math.min(x.length, y.length); i++) {
		const d = (x[i].codePointAt(0) ?? 0) - (y[i].codePointAt(0) ?? 0);
		if (d !== 0) return d;
	}
	return x.length - y.length;
}

function renderMapping(map: { [key: string]: Json }, indent: number): string {
	const keys = Object.keys(map).sort(compareCodePoints);
	if (keys.length === 0) return '{}';
	const entries = keys.map((k) => `${pad(indent + 1)}${keyText(k)}: ${render(map[k], indent + 1)},\n`);
	return `{\n${entries.join('')}${pad(indent)}}`;
}

function renderSequence(list: Json[], indent: number): string {
	if (list.length === 0) return '[]';
	if (list.every(isContainer)) return `[${list.map((item) => render(item, indent)).join(', ')}]`;
	const items = list.map((item) => `${pad(indent + 1)}${render(item, indent + 1)},\n`);
	return `[\n${items.join('')}${pad(indent)}]`;
}

function render(value: Json, indent: number): string {
	if (typeof value === 'string') return quote(value, indent + 1, false);
	if (Array.isArray(value)) return renderSequence(value, indent);
	if (isContainer(value)) return renderMapping(value, indent);
	return String(value);
}

/**
 * The value as a KYAML document: the `---` header, the value, and a newline, as `kubectl get -o
 * kyaml` writes it. Undefined, functions and dates are read as JSON would read them.
 */
export function toKyaml(value: unknown): string {
	const json = JSON.stringify(value);
	const plain: Json = json === undefined ? null : (JSON.parse(json) as Json);
	return `${HEADER}${render(plain, 0)}\n`;
}
