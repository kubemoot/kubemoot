<script lang="ts">
	import { phaseStatus } from '$lib/resource-status';
	import { resolve } from '$app/paths';
	import type { MCPCatalog } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		catalog: MCPCatalog;
		showNamespace?: boolean;
	}

	let { catalog, showNamespace = false }: Props = $props();

	const status = $derived(phaseStatus(catalog.status?.phase));

	const statusLabel = $derived(catalog.status?.phase || 'Unknown');
	const discovered = $derived(catalog.status?.serversDiscovered ?? 0);
</script>

<ResourceCard
	name={catalog.metadata.name}
	kind="MCPCatalog"
	href="{resolve('/mcpcatalogs/[name]', { name: catalog.metadata.name })}?namespace={catalog.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? catalog.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Type</span>
				<span class="value type-badge">{catalog.spec.type}</span>
			</div>
			<div class="row">
				<span class="label">URL</span>
				<span class="value mono">{catalog.spec.url}</span>
			</div>
			<div class="row">
				<span class="label">Sync Interval</span>
				<span class="value">{catalog.spec.syncInterval || '24h'}</span>
			</div>
			<div class="row">
				<span class="label">Discovered</span>
				<span class="value">{discovered} servers</span>
			</div>
		</div>
	{/snippet}

	{#snippet footer()}
		<div class="footer-row">
			<div class="stats">
				<span class="stat allowed">{catalog.status?.serversAllowed ?? 0} allowed</span>
				<span class="stat blocked">{catalog.status?.serversBlocked ?? 0} blocked</span>
			</div>
			{#if catalog.spec.queries && catalog.spec.queries.length > 0}
				<div class="queries">
					{#each catalog.spec.queries.slice(0, 2) as query}
						<span class="query-tag">{query}</span>
					{/each}
					{#if catalog.spec.queries.length > 2}
						<span class="more">+{catalog.spec.queries.length - 2}</span>
					{/if}
				</div>
			{/if}
		</div>
	{/snippet}
</ResourceCard>

<style>
	.info {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.8rem;
	}

	.label {
		color: var(--color-text-muted);
	}

	.value {
		color: var(--color-text);
	}

	.value.mono {
		font-family: var(--font-mono);
		font-size: 0.7rem;
		max-width: 140px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.type-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		font-family: var(--font-mono);
	}

	.footer-row {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.stats {
		display: flex;
		gap: 0.5rem;
	}

	.stat {
		font-size: 0.7rem;
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.stat.allowed {
		background-color: rgba(34, 197, 94, 0.15);
		color: var(--color-success);
	}

	.stat.blocked {
		background-color: rgba(239, 68, 68, 0.15);
		color: var(--color-error);
	}

	.queries {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}

	.query-tag {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		font-size: 0.65rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}

	.more {
		color: var(--color-text-muted);
		font-size: 0.65rem;
	}
</style>
