// Splits the text toKyaml() writes into coloured pieces, one array per line, with the same token
// kinds as the YAML view so both share one set of colours. KYAML is braces, brackets and commas
// around one entry per line, so a line is an optional key, then values and punctuation; a string
// folded over several lines is tracked from the line that opens it to the line that closes it.

import { quotedEnd, type YamlToken, type YamlTokenKind } from '#lib/yaml-highlight.js';

const BARE_KEY = /^[^\s"{}[\],:]+(?=: )/;
const PUNCTUATION = new Set(['[', ']', '{', '}', ',', ' ']);

function push(tokens: YamlToken[], kind: YamlTokenKind, text: string) {
	if (text) tokens.push({ kind, text });
}

function wordKind(word: string): YamlTokenKind {
	if (word === 'true' || word === 'false') return 'bool';
	if (word === 'null') return 'null';
	return Number.isFinite(Number(word)) ? 'number' : 'plain';
}

interface Scanned {
	tokens: YamlToken[];
	/** The line ends inside a string that continues on the next line. */
	open: boolean;
}

/** The end of the run starting at `at` that is all punctuation, or all not punctuation. */
function runEnd(text: string, at: number): number {
	const punctuation = PUNCTUATION.has(text[at]);
	let end = at;
	while (end < text.length && PUNCTUATION.has(text[end]) === punctuation) end++;
	return end;
}

/** The values and punctuation of a line from `text`, which does not start inside a string. */
function scanValues(text: string): Scanned {
	const tokens: YamlToken[] = [];
	let at = 0;
	while (at < text.length) {
		if (text[at] === '"') {
			const end = quotedEnd(text, at);
			push(tokens, 'string', text.slice(at, end < 0 ? undefined : end));
			if (end < 0) return { tokens, open: true };
			at = end;
			continue;
		}
		const end = runEnd(text, at);
		const run = text.slice(at, end);
		push(tokens, PUNCTUATION.has(run[0]) ? 'punct' : wordKind(run), run);
		at = end;
	}
	return { tokens, open: false };
}

/** The key at the start of the body, with the colon after it: [key text, length including ": "], or undefined. */
function leadingKey(body: string): [string, number] | undefined {
	if (body.startsWith('"')) {
		const end = quotedEnd(body, 0);
		return end > 0 && body.startsWith(': ', end) ? [body.slice(0, end), end + 2] : undefined;
	}
	const bare = BARE_KEY.exec(body);
	return bare ? [bare[0], bare[0].length + 2] : undefined;
}

/** One line as tokens whose texts join back to the line, and whether a string stays open after it. */
export function highlightKyamlLine(line: string, inString = false): Scanned {
	const tokens: YamlToken[] = [];
	const indent = line.length - line.trimStart().length;
	push(tokens, 'plain', line.slice(0, indent));
	const body = line.slice(indent);
	if (inString) {
		// A continuation line is the inside of a string: scan it as one that opened just before it.
		const end = quotedEnd(`"${body}`, 0) - 1;
		push(tokens, 'string', body.slice(0, end < 0 ? undefined : end));
		if (end < 0) return { tokens, open: true };
		const rest = scanValues(body.slice(end));
		return { tokens: [...tokens, ...rest.tokens], open: rest.open };
	}
	const key = leadingKey(body);
	if (key) {
		push(tokens, 'key', key[0]);
		push(tokens, 'punct', ':');
		push(tokens, 'plain', ' ');
	}
	const values = scanValues(body.slice(key ? key[1] : 0));
	return { tokens: [...tokens, ...values.tokens], open: values.open };
}

/** Every line of the text (without its newline) as tokens. */
export function highlightKyaml(text: string): YamlToken[][] {
	const lines = text.split('\n');
	if (lines.at(-1) === '') lines.pop();
	let open = false;
	return lines.map((line) => {
		if (line === '---') return [{ kind: 'punct' as const, text: line }];
		const scanned = highlightKyamlLine(line, open);
		open = scanned.open;
		return scanned.tokens;
	});
}
