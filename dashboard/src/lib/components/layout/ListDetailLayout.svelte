<script lang="ts">
	import type { Snippet } from 'svelte';
	import PageTitle from '#lib/components/common/PageTitle.svelte';
	import type { PageRoute } from '#lib/page-definitions.js';

	// The one layout of a page that lists things on the left and opens one on the right: the
	// Moot and the Conversations page. Both use it, so the title, the list column, the open
	// item and the way they stack on a narrow screen cannot drift apart.
	interface Props {
		/** The route id of the page: its title and the tooltip that says what the term means come from it. */
		route: PageRoute;
		/** One muted line under the title. */
		subtitle: string;
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

	let { route, subtitle, actions, list, detail, onlistscroll, detailEl = $bindable(null), detailOpen = true }: Props = $props();
</script>

<div class="list-detail" class:no-detail={!detailOpen}>
	<div class="page-header">
		<div class="title">
			<PageTitle {route} />
			<p class="subtitle">{subtitle}</p>
		</div>
		{#if actions}<div class="header-actions">{@render actions()}</div>{/if}
	</div>

	<div class="columns">
		<div class="list" onscroll={onlistscroll}>{@render list()}</div>
		<div class="detail" bind:this={detailEl}>{@render detail()}</div>
	</div>
</div>

<style>
	.list-detail { display: flex; flex-direction: column; height: 100%; padding: 1.5rem; gap: 1rem; min-width: 0; }
	.page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
	.subtitle { margin: 0.4rem 0 0; font-size: 0.8rem; color: var(--color-text-muted); }
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
