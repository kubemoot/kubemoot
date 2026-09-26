<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		label: string;
		value?: string | number | null;
		children?: Snippet;
		mono?: boolean;
	}

	let { label, value, children, mono = false }: Props = $props();
</script>

<div class="info-row">
	<span class="label">{label}</span>
	<span class="value" class:mono>
		{#if children}
			{@render children()}
		{:else if value !== null && value !== undefined}
			{value}
		{:else}
			<span class="empty">-</span>
		{/if}
	</span>
</div>

<style>
	.info-row {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--color-border);
	}

	.info-row:last-child {
		border-bottom: none;
	}

	.label {
		color: var(--color-text-muted);
		font-size: 0.85rem;
		flex-shrink: 0;
	}

	.value {
		color: var(--color-text);
		font-size: 0.85rem;
		text-align: right;
		word-break: break-all;
		max-width: 60%;
	}

	.value.mono {
		font-family: var(--font-mono);
		font-size: 0.8rem;
	}

	.empty {
		color: var(--color-text-muted);
	}
</style>
