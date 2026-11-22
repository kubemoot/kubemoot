// The reason an operator gives for acting on another person's conversation: the header the
// gateway reads, and what makes a reason valid. The browser and the dashboard server both
// check with this, so a reason the page lets through is one the server accepts.

export const REASON_HEADER = 'Kubemoot-Access-Reason';
export const MAX_REASON_LENGTH = 200;

// eslint-disable-next-line no-control-regex -- a reason becomes a header value: control characters are refused
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f]/;

/** The reason trimmed, when it is 1 to 200 characters with no control character; else undefined. */
export function validReason(reason: string | null | undefined): string | undefined {
	const text = (reason ?? '').trim();
	return text !== '' && text.length <= MAX_REASON_LENGTH && !CONTROL_CHARACTERS.test(text) ? text : undefined;
}
