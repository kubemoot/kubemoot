// The search syntax of the conversation and discussion lists, one parser for both:
//   namespace          every word must be found (case-insensitive), anywhere in the question or messages
//   namespace -smoke   a word with a leading "-" must not be found
//   "gpu temperature"  a quoted phrase is found as a phrase
//   -"smoke check"     an excluded phrase
//   -smoke             only exclusions work too; an empty query matches everything

export interface SearchTerms {
	/** Lower-cased words and phrases that must all be found. */
	include: string[];
	/** Lower-cased words and phrases of which none may be found. */
	exclude: string[];
}

export const SEARCH_HELP =
	'Words must all match. -word leaves out matches. "a phrase" matches as written. Example: namespace -smoke "gpu temperature"';

/** Reads a quoted phrase that starts after the opening quote; an unclosed quote runs to the end. */
function readPhrase(text: string, from: number): { phrase: string; next: number } {
	const close = text.indexOf('"', from);
	return close === -1
		? { phrase: text.slice(from), next: text.length }
		: { phrase: text.slice(from, close), next: close + 1 };
}

/** Reads a bare word, which ends at whitespace. */
function readWord(text: string, from: number): { word: string; next: number } {
	let end = from;
	while (end < text.length && !/\s/.test(text[end])) end++;
	return { word: text.slice(from, end), next: end };
}

/** The words and phrases of a query, split into what must be found and what must not. */
export function parseSearch(query: string): SearchTerms {
	const terms: SearchTerms = { include: [], exclude: [] };
	let at = 0;
	while (at < query.length) {
		if (/\s/.test(query[at])) {
			at++;
			continue;
		}
		const negated = query[at] === '-';
		if (negated) at++;
		let text: string;
		if (query[at] === '"') {
			const read = readPhrase(query, at + 1);
			text = read.phrase;
			at = read.next;
		} else {
			const read = readWord(query, at);
			text = read.word;
			at = read.next;
		}
		const term = text.trim().toLowerCase();
		if (term !== '') (negated ? terms.exclude : terms.include).push(term);
	}
	return terms;
}

/** True when the query puts no condition on the text. */
export function isEmptySearch(terms: SearchTerms): boolean {
	return terms.include.length === 0 && terms.exclude.length === 0;
}

/**
 * Whether the texts of one thread (its question and its messages) satisfy the query. A phrase
 * is found inside one text, a word anywhere in the thread.
 */
export function matchesSearch(texts: readonly string[], terms: SearchTerms): boolean {
	if (isEmptySearch(terms)) return true;
	const lower = texts.map((t) => t.toLowerCase());
	const found = (term: string) => lower.some((t) => t.includes(term));
	return terms.include.every(found) && !terms.exclude.some(found);
}
