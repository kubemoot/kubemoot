<script lang="ts">
	import UsageBar from '../common/UsageBar.svelte';
	import type { AcceleratorStatus } from '#lib/accelerator-status.js';

	interface Props {
		status: AcceleratorStatus;
	}

	let { status }: Props = $props();

	const bars = $derived([status.busy, status.memory].filter((b) => b !== undefined));
</script>

<footer class="accelerator" data-testid="accelerator">
	<span class="type">{status.label}</span>
	{#if status.unavailable}
		<p class="line muted">{status.unavailable}</p>
	{:else}
		{#if status.serving?.kind === 'server'}
			<p class="line" data-testid="serving">
				<span class="server">{status.serving.name}</span>
				{#if status.serving.models.length > 0}<span class="models">{status.serving.models.join(', ')}</span>{:else}<span class="models">no model loaded</span>{/if}
			</p>
		{:else if status.serving?.kind === 'pod'}
			<p class="line" data-testid="serving">Held by {status.serving.pod}</p>
		{:else if status.serving?.kind === 'free'}
			<p class="line" data-testid="serving">Free</p>
		{/if}
		{#each bars as b (b.label)}
			<UsageBar label={b.label} text={b.text} fraction={b.fraction} level={b.level} />
		{/each}
	{/if}
</footer>

<style>
	.accelerator {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		margin: 0 -1rem -1rem;
		padding: 0.75rem 1rem;
		background-color: rgba(168, 85, 247, 0.1);
		border-top: 1px solid rgba(168, 85, 247, 0.2);
		border-radius: 0 0 0.5rem 0.5rem;
	}
	.type {
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--color-purple);
	}
	.line {
		margin: 0;
		font-size: 0.8rem;
		color: var(--color-text);
		overflow-wrap: anywhere;
	}
	.line.muted,
	.models {
		color: var(--color-text-muted);
	}
	.server {
		font-weight: 500;
		margin-right: 0.5rem;
	}
</style>
