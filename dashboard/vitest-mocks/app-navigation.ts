import { setPageUrl } from './app-state.svelte.js';

/** Test stand-in for $app/navigation goto(): moves the mocked page to the URL. */
export async function goto(url: string | URL): Promise<void> {
	setPageUrl(String(url));
}
