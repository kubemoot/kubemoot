<script lang="ts">
	import type { Snippet } from 'svelte';
	import HoverActions from '#lib/components/common/HoverActions.svelte';
	import type { HoverAction } from '#lib/hover-actions.js';

	// One card of the list: badges on top, the question, a line of facts below.
	interface Props {
		selected: boolean;
		onopen: () => void;
		/** The badges and the status. */
		head?: Snippet;
		/** The question the card is about. */
		children: Snippet;
		/** The facts line: counts and time. */
		foot?: Snippet;
		/** Left of the card, such as a tick box. */
		leading?: Snippet;
		/** The row's actions: an icon bar over the card while the pointer or the focus is on the row. */
		actions?: HoverAction[];
	}

	let { selected, onopen, head, children, foot, leading, actions = [] }: Props = $props();
</script>

<div class="row has-hover-actions">
	{#if leading}{@render leading()}{/if}
	<button type="button" class="card" class:selected onclick={onopen}>
		{#if head}<div class="head">{@render head()}</div>{/if}
		<div class="query">{@render children()}</div>
		{#if foot}<div class="foot">{@render foot()}</div>{/if}
	</button>
	{#if actions.length > 0}<HoverActions actions={actions} label="Row actions" />{/if}
</div>

<style>
	.row { position: relative; display: flex; align-items: center; gap: 0.35rem; }
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
