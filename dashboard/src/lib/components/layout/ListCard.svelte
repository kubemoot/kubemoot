<script lang="ts">
	import type { Snippet } from 'svelte';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';

	// One card of the list: badges on top, the question, a line of facts below.
	interface Props {
		selected: boolean;
		/** What the card opens, for its tooltip. */
		kind: 'thread' | 'conversation';
		onopen: () => void;
		/** The badges and the status. */
		head?: Snippet;
		/** The question the card is about. */
		children: Snippet;
		/** The facts line: counts and time. */
		foot?: Snippet;
		/** Left of the card, such as a tick box. */
		leading?: Snippet;
	}

	let { selected, kind, onopen, head, children, foot, leading }: Props = $props();
</script>

<div class="row">
	{#if leading}{@render leading()}{/if}
	<button type="button" class="card" class:selected onclick={onopen} data-tooltip-placement="right" use:tooltip={ACTIONS.openListItem(kind)}>
		{#if head}<div class="head">{@render head()}</div>{/if}
		<div class="query">{@render children()}</div>
		{#if foot}<div class="foot">{@render foot()}</div>{/if}
	</button>
</div>

<style>
	.row { display: flex; align-items: center; gap: 0.35rem; }
	.card {
		flex: 1; min-width: 0; background: var(--color-bg-secondary); border: 1px solid var(--color-border);
		border-radius: 0.5rem; padding: 0.75rem; cursor: pointer; text-align: left; width: 100%;
		transition: all 0.15s; color: var(--color-text); font-family: inherit; font-size: inherit;
	}
	.card:hover { border-color: var(--color-primary); }
	.card.selected { border-color: var(--color-primary); background: rgba(59, 130, 246, 0.1); }
	.head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 0.3rem 0.4rem; margin-bottom: 0.5rem; }
	.query {
		font-size: 0.8rem; color: var(--color-text); margin-bottom: 0.5rem; overflow-wrap: anywhere;
		display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
	}
	.foot { display: flex; justify-content: space-between; gap: 0.5rem; font-size: 0.7rem; color: var(--color-text-muted); }
</style>
