<script lang="ts">
	import { pageDefinition, type PageRoute } from '#lib/page-definitions.js';
	import { tooltip } from '#lib/tooltip.js';

	interface Props {
		/** The route id of the page, a key of the definitions module. */
		route: PageRoute;
		/** Replaces the defined title, for detail pages that show the resource name. */
		title?: string;
		/** Monospace, for a resource name. */
		mono?: boolean;
	}

	let { route, title, mono = false }: Props = $props();

	const def = $derived(pageDefinition(route));
</script>

<h1 class="page-title-text" class:mono>
	<span class="term" tabindex="0" use:tooltip={`${def.term}\n${def.purpose}`}>{title ?? def.title}</span>
</h1>

<style>
	.page-title-text {
		display: inline-block;
		margin: 0;
		font-size: 1.5rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.page-title-text.mono {
		font-family: var(--font-mono);
	}

	.term {
		cursor: help;
		text-decoration: underline dotted var(--color-text-muted);
		text-underline-offset: 0.3em;
		border-radius: 3px;
	}

	.term:focus-visible {
		outline: 2px solid var(--color-primary);
		outline-offset: 3px;
	}
</style>
