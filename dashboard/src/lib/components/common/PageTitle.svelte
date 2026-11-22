<script lang="ts">
	import type { Snippet } from 'svelte';
	import { pageDefinition, type PageRoute } from '#lib/page-definitions.js';
	import InfoPopover from './InfoPopover.svelte';

	interface Props {
		/** The route id of the page, a key of the definitions module. */
		route: PageRoute;
		/** Replaces the defined title, for detail pages that show the resource name. */
		title?: string;
		/** Monospace, for a resource name. */
		mono?: boolean;
		/** Lines added to the (i) help after the page's own. */
		notes?: readonly string[];
		/** Extra content at the end of the (i) help, such as a link. */
		extra?: Snippet;
	}

	let { route, title, mono = false, notes = [], extra }: Props = $props();

	const def = $derived(pageDefinition(route));
	const shown = $derived(title ?? def.title);
	const lines = $derived([def.term, def.purpose, ...(def.notes ?? []), ...notes]);
</script>

<span class="page-title">
	<h1 class="page-title-text" class:mono>{shown}</h1>
	<InfoPopover label="About {shown}" {lines} children={extra} />
</span>

<style>
	.page-title {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
	}

	.page-title-text {
		margin: 0;
		font-size: 1.5rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.page-title-text.mono {
		font-family: var(--font-mono);
	}
</style>
