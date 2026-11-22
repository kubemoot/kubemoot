// Splits the text JSON.stringify(value, null, 2) writes into coloured pieces, one array per line.
// It reads only that shape (one key or value per line, an optional trailing comma), so a few
// string steps are enough and no general JSON highlighter is needed. The pieces use the same
// kinds as the YAML view, so both share one set of colours.

import type { YamlToken, YamlTokenKind } from '#lib/yaml-highlight.js';

function scalarKind(value: string): YamlTokenKind {
	if (value === 'true' || value === 'false') return 'bool';
	if (value === 'null') return 'null';
	if (value.startsWith('"')) return 'string';
	return Number.isFinite(Number(value)) ? 'number' : 'plain';
}

function push(tokens: YamlToken[], kind: YamlTokenKind, text: string) {
	if (text) tokens.push({ kind, text });
}

/** Index just past the double-quoted string starting at `start`, or -1 when it never closes. */
function quotedEnd(line: string, start: number): number {
	for (let i = start + 1; i < line.length; i++) {
		if (line[i] === '\\') i++;
		else if (line[i] === '"') return i + 1;
	}
	return -1;
}

/** The value part of a line: a scalar or an opening or closing bracket, with its trailing comma apart. */
function pushValue(tokens: YamlToken[], rest: string) {
	const comma = rest.endsWith(',') ? ',' : '';
	const value = comma ? rest.slice(0, -1) : rest;
	const isBracket = /^[[\]{}]+$/.test(value) || value === '[]' || value === '{}';
	push(tokens, isBracket ? 'punct' : scalarKind(value), value);
	push(tokens, 'punct', comma);
}

/** One line of pretty-printed JSON as tokens whose texts join back to the line exactly. */
export function highlightJsonLine(line: string): YamlToken[] {
	const tokens: YamlToken[] = [];
	const indent = line.length - line.trimStart().length;
	push(tokens, 'plain', line.slice(0, indent));
	const body = line.slice(indent);
	const end = body.startsWith('"') ? quotedEnd(body, 0) : -1;
	if (end > 0 && body.startsWith(': ', end)) {
		push(tokens, 'key', body.slice(0, end));
		push(tokens, 'punct', ':');
		push(tokens, 'plain', ' ');
		pushValue(tokens, body.slice(end + 2));
		return tokens;
	}
	pushValue(tokens, body);
	return tokens;
}

/** Every line of the text (without its newline) as tokens. */
export function highlightJson(text: string): YamlToken[][] {
	const lines = text.split('\n');
	if (lines.at(-1) === '') lines.pop();
	return lines.map(highlightJsonLine);
}
