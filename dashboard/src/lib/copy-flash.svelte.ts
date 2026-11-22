import { copyText } from '#lib/clipboard.js';

/** How long "Copied" shows after a copy. */
export const COPIED_MS = 1500;

/**
 * Copies text and remembers for a moment what was copied, so a page can say "Copied" beside it.
 * One flash serves a whole list: `copied` holds the key of the last copy.
 */
export class CopyFlash {
	copied = $state<string | null>(null);
	#timer: ReturnType<typeof setTimeout> | undefined;

	/** Copies `text`; true when the browser allowed it, false (and no "Copied") when it did not. */
	async copy(key: string, text: string): Promise<boolean> {
		try {
			await copyText(text);
		} catch {
			return false;
		}
		this.copied = key;
		clearTimeout(this.#timer);
		this.#timer = setTimeout(() => {
			if (this.copied === key) this.copied = null;
		}, COPIED_MS);
		return true;
	}
}
