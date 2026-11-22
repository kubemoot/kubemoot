<script lang="ts">
	import { onMount, type Snippet } from 'svelte';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import { loadCollapsed, saveCollapsed } from '#lib/list-collapse.js';
	import PageTitle from '#lib/components/common/PageTitle.svelte';
	import type { PageRoute } from '#lib/page-definitions.js';

	// The one layout of a page that lists things on the left and opens one on the right: the
	// Moot and the Conversations page. Both use it, so the title, the list column, the open
	// item and the way they stack on a narrow screen cannot drift apart.
	interface Props {
		/** The route id of the page: its title and the tooltip that says what the term means come from it. */
		route: PageRoute;
		/** Right of the title: a status or a control for the whole page. */
		actions?: Snippet;
		/** The left column: the filter bar, then the cards. */
		list: Snippet;
		/** The right column: the open item. */
		detail: Snippet;
		onlistscroll?: (event: Event) => void;
		/** The open item's scroll container, for a page that follows new content. */
		detailEl?: HTMLElement | null;
		/** Something is open on the right. When nothing is, a narrow screen gives the list the whole height. */
		detailOpen?: boolean;
	}

	let { route, actions, list, detail, onlistscroll, detailEl = $bindable(null), detailOpen = true }: Props = $props();

	// The viewer's choice, kept in this browser. The list is hidden only while something is open
	// beside it: with nothing open the list is the page.
	let chosen = $state(false);
	const collapsed = $derived(chosen && detailOpen);

	onMount(() => {
		chosen = loadCollapsed(route);
	});

	function toggle() {
		chosen = !chosen;
		saveCollapsed(route, chosen);
	}
</script>

<div class="list-detail" class:no-detail={!detailOpen}>
	<div class="page-header">
		<div class="title">
			{#if detailOpen}
				<button type="button" class="collapse" aria-label={collapsed ? 'Show the list' : 'Hide the list'} aria-controls="list-column" aria-expanded={!collapsed} use:tooltip={ACTIONS.listToggle(collapsed)} onclick={toggle}>
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
						<rect x="3" y="4" width="18" height="16" rx="2" />
						<line x1="9" y1="4" x2="9" y2="20" />
						{#if collapsed}<polyline points="13 9 16 12 13 15" />{:else}<polyline points="16 9 13 12 16 15" />{/if}
					</svg>
				</button>
			{/if}
			<PageTitle {route} />
		</div>
		{#if actions}<div class="header-actions">{@render actions()}</div>{/if}
	</div>

	<div class="columns">
		<div class="list" id="list-column" class:collapsed inert={collapsed} onscroll={onlistscroll}>{@render list()}</div>
		<div class="detail" bind:this={detailEl}>{@render detail()}</div>
	</div>
</div>

<style>
	.list-detail { display: flex; flex-direction: column; height: 100%; padding: 1.5rem; gap: 1rem; min-width: 0; }
	.page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
	.title { display: flex; align-items: center; gap: 0.5rem; min-width: 0; }
	.collapse { display: flex; align-items: center; justify-content: center; width: 32px; height: 32px; flex-shrink: 0; background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.375rem; color: var(--color-text-muted); cursor: pointer; }
	.collapse:hover { background: var(--color-bg-tertiary); color: var(--color-text); border-color: var(--color-primary); }
	.collapse:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 2px; }
	.list.collapsed { display: none; }
	.header-actions { display: flex; align-items: center; gap: 1rem; }
	.columns { display: flex; gap: 1rem; flex: 1; min-height: 0; }
	.list { width: 360px; flex-shrink: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 0.5rem; }
	.detail { flex: 1; min-width: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 1rem; }

	/* On a narrow screen the list sits above the open item, and each scrolls on its own. */
	@media (max-width: 1100px) {
		.columns { flex-direction: column; }
		.list { width: 100%; max-height: 30vh; flex-shrink: 0; }
		.no-detail .list { max-height: none; flex: 1; }
		.no-detail .detail { display: none; }
		.detail { flex: 1; min-height: 0; }
	}
</style>
