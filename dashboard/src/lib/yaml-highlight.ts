// Splits the text toYaml() writes into coloured pieces. It reads only the shapes
// that writer produces (block mappings, dashed items, quoted or plain scalars), so
// a few string steps are enough and no general YAML highlighter is needed.

export type YamlTokenKind = 'key' | 'string' | 'number' | 'bool' | 'null' | 'punct' | 'plain';

export interface YamlToken {
	kind: YamlTokenKind;
	text: string;
}

function scalarKind(value: string): YamlTokenKind {
	if (value === 'true' || value === 'false') return 'bool';
	if (value === 'null' || value === '~') return 'null';
	if (value === '[]' || value === '{}') return 'punct';
	if (Number.isFinite(Number(value))) return 'number';
	return 'string';
}

/** Length of the indent and dashes that open a line: spaces, then any number of "- ". */
function leadLength(line: string): number {
	let at = 0;
	while (line[at] === ' ') at++;
	while (line.startsWith('- ', at)) at += 2;
	return at;
}

/** Index just past a double-quoted string starting at `start`, or -1 when it never closes. */
function quotedEnd(line: string, start: number): number {
	for (let i = start + 1; i < line.length; i++) {
		if (line[i] === '\\') i++;
		else if (line[i] === '"') return i + 1;
	}
	return -1;
}

/** Index of the colon that ends the key starting at `start`, or -1 when the line has no key. */
function keyColon(line: string, start: number): number {
	if (line[start] === '"') {
		const end = quotedEnd(line, start);
		return end > 0 && line[end] === ':' ? end : -1;
	}
	const at = line.indexOf(':', start);
	if (at <= start) return -1;
	return at === line.length - 1 || line[at + 1] === ' ' ? at : -1;
}

function push(tokens: YamlToken[], kind: YamlTokenKind, text: string) {
	if (text) tokens.push({ kind, text });
}

/** One line of YAML as tokens whose texts join back to the line exactly. */
export function highlightLine(line: string): YamlToken[] {
	const tokens: YamlToken[] = [];
	const start = leadLength(line);
	push(tokens, 'punct', line.slice(0, start));
	const colon = keyColon(line, start);
	if (colon < 0) {
		const rest = line.slice(start);
		push(tokens, rest.trim() === '' ? 'plain' : scalarKind(rest), rest);
		return tokens;
	}
	push(tokens, 'key', line.slice(start, colon));
	push(tokens, 'punct', ':');
	const value = line.slice(colon + 1);
	if (value.startsWith(' ')) {
		push(tokens, 'plain', ' ');
		push(tokens, scalarKind(value.slice(1)), value.slice(1));
	} else {
		push(tokens, 'plain', value);
	}
	return tokens;
}

/** Every line of the text (without its newline) as tokens. */
export function highlightYaml(text: string): YamlToken[][] {
	const lines = text.split('\n');
	if (lines.at(-1) === '') lines.pop();
	return lines.map(highlightLine);
}
