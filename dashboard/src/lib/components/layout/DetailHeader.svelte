<script lang="ts">
	import type { Snippet } from 'svelte';

	// The header of the open item: what it is and its state on the left, its actions on the right.
	interface Props {
		/** The crew, the status and any other badge. */
		children: Snippet;
		/** The icon buttons and the actions menu. */
		actions?: Snippet;
	}

	let { children, actions }: Props = $props();
</script>

<div class="detail-header">
	<div class="lead">{@render children()}</div>
	{#if actions}<div class="end">{@render actions()}</div>{/if}
</div>

<style>
	.detail-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; min-height: 1.75rem; }
	.lead { display: flex; align-items: center; flex-wrap: wrap; gap: 0.6rem 1rem; min-width: 0; }
	.end { display: flex; align-items: center; gap: 0.35rem; flex-shrink: 0; }
	/* The icon buttons of the header look the same on every page that uses it. */
	.end :global(.action-btn) {
		background: none; border: none; padding: 0.25rem 0.375rem; cursor: pointer; font-size: 0.875rem;
		opacity: 0.7; transition: opacity 0.2s; border-radius: 0.25rem; color: var(--color-text-muted);
	}
	.end :global(.action-btn:hover) { opacity: 1; background-color: var(--color-bg-secondary); }
	.end :global(.action-btn.active-toggle) { color: var(--accent-blue); opacity: 1; background-color: rgba(59, 130, 246, 0.1); }
</style>
