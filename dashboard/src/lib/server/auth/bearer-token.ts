import { inspect } from 'node:util';

const REDACTED = '[redacted]';

/**
 * A Kubernetes bearer token that cannot leak by accident: printing it, interpolating it into
 * a string, logging it, or serialising it (JSON, an error report, a trace attribute) shows
 * "[redacted]". The one way to read it is reveal(), called where the token is sent to the
 * Kubernetes API server or to a crew's gateway.
 */
export class BearerToken {
	readonly #value: string;

	constructor(value: string) {
		this.#value = value;
	}

	/** The token itself, for the Authorization header of one outgoing request. */
	reveal(): string {
		return this.#value;
	}

	toString(): string {
		return REDACTED;
	}

	toJSON(): string {
		return REDACTED;
	}

	[inspect.custom](): string {
		return REDACTED;
	}
}
