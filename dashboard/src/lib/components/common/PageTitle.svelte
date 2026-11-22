<script lang="ts">
	import { pageDefinition, type PageRoute } from '#lib/page-definitions.js';

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
	const uid = $props.id();
	const tipId = `page-tip-${uid}`;
	let hovered = $state(false);
	let focused = $state(false);
	let dismissed = $state(false);
	const shown = $derived((hovered || focused) && !dismissed);

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') dismissed = true;
	}
</script>

<svelte:window onkeydown={onKeydown} />

<span
	class="page-title"
	role="presentation"
	onmouseenter={() => { hovered = true; dismissed = false; }}
	onmouseleave={() => { hovered = false; dismissed = false; }}
>
	<h1 class="page-title-text" class:mono>
		<span
			class="term"
			tabindex="0"
			aria-describedby={tipId}
			onfocus={() => { focused = true; dismissed = false; }}
			onblur={() => { focused = false; }}
		>{title ?? def.title}</span>
	</h1>
	<span id={tipId} role="tooltip" class="page-tip" class:shown>
		<span class="tip-term">{def.term}</span>
		<span class="tip-purpose">{def.purpose}</span>
	</span>
</span>

<style>
	.page-title {
		position: relative;
		display: inline-block;
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

	.page-tip {
		position: absolute;
		top: 100%;
		margin-top: 6px;
		left: 0;
		z-index: 100;
		display: none;
		flex-direction: column;
		gap: 0.35rem;
		width: max-content;
		max-width: min(360px, 90vw);
		padding: 0.6rem 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
		font-size: 0.8rem;
		font-weight: 400;
		line-height: 1.45;
		color: var(--color-text);
	}

	.page-tip::before {
		content: '';
		position: absolute;
		top: -6px;
		left: 0;
		right: 0;
		height: 6px;
	}

	.page-tip.shown {
		display: flex;
	}

	.tip-purpose {
		color: var(--color-text-muted);
	}
</style>
