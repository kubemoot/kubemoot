<script lang="ts">
	/** Two sheets, or a check once copied; drawn in the text colour so it reads in both themes. */
	let { done = false }: { done?: boolean } = $props();
</script>

<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
	{#if done}
		<polyline points="20 6 9 17 4 12" />
	{:else}
		<rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
	{/if}
</svg>
