<script lang="ts">
	// Shown over an open item the list's filter would not list: the item stays open, the filter stays
	// as the person set it, and one click clears it.
	let { onshowall }: { onshowall: () => void } = $props();
</script>

<div class="outside-filter" role="status">
	<span>Outside the current filter</span>
	<button type="button" class="show-all" onclick={onshowall}>Show all</button>
</div>

<style>
	.outside-filter {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
		padding: 0.3rem 0.6rem;
		font-size: 0.8rem;
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		border-radius: 0.4rem;
	}
	.show-all {
		border: 0;
		background: none;
		padding: 0;
		font: inherit;
		color: var(--color-primary);
		cursor: pointer;
		text-decoration: underline;
	}
</style>
