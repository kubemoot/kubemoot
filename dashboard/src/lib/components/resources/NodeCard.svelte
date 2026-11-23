<script lang="ts">
	import { resolve } from '$app/paths';
	import HealthBadge from '../common/HealthBadge.svelte';
	import UsageBar from '../common/UsageBar.svelte';
	import AcceleratorSection from './AcceleratorSection.svelte';
	import type { NodeSummary } from '#lib/node-summary.js';

	interface Props {
		summary: NodeSummary;
	}

	let { summary }: Props = $props();

	const bars = $derived([summary.cpu, summary.memory, summary.pods].filter((b) => b !== undefined));
</script>

<a class="card" href={resolve('/nodes/[name]', { name: summary.name })} data-testid="node-{summary.name}">
	<header class="header">
		<h3 class="name">{summary.name}</h3>
		<HealthBadge health={summary.health} />
	</header>

	{#if summary.problems.length > 0}
		<ul class="problems" class:offline={summary.offline}>
			{#each summary.problems as p (p)}<li>{p}</li>{/each}
		</ul>
	{/if}

	{#if bars.length > 0}
		<div class="bars">
			{#each bars as b (b.label)}
				<UsageBar label={b.label} text={b.text} fraction={b.fraction} level={b.level} />
			{/each}
		</div>
	{/if}

	{#if summary.podsNote}
		<p class="pods-note">Pods: {summary.podsNote}</p>
	{/if}

	{#if summary.accelerator}
		<AcceleratorSection status={summary.accelerator} />
	{/if}
</a>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1rem;
		color: inherit;
		text-decoration: none;
		transition: border-color 0.15s;
	}
	.card:hover {
		border-color: var(--color-primary);
		text-decoration: none;
	}
	.header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.name {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text);
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.problems {
		list-style: none;
		margin: 0;
		padding: 0.4rem 0.6rem;
		border-radius: 0.375rem;
		background: rgba(239, 68, 68, 0.1);
		font-size: 0.75rem;
		color: var(--color-text);
	}
	.problems.offline {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}
	.bars {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.pods-note {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
