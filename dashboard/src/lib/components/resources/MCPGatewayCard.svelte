<script lang="ts">
	import { readinessStatus } from '$lib/resource-status';
	import { resolve } from '$app/paths';
	import type { MCPGateway } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		gateway: MCPGateway;
		showNamespace?: boolean;
	}

	let { gateway, showNamespace = false }: Props = $props();

	const status = $derived(readinessStatus(gateway.status));

	const statusLabel = $derived(gateway.status?.phase || 'Unknown');
	const serverCount = $derived(gateway.status?.registeredServers?.length || 0);
</script>

<ResourceCard
	name={gateway.metadata.name}
	kind="MCPGateway"
	href="{resolve('/mcpgateways/[name]', { name: gateway.metadata.name })}?namespace={gateway.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? gateway.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Implementation</span>
				<span class="value">{gateway.spec.implementation || 'kubemoot'}</span>
			</div>
			{#if gateway.status?.endpoint}
				<div class="row">
					<span class="label">Endpoint</span>
					<span class="value mono">{gateway.status.endpoint}</span>
				</div>
			{/if}
			<div class="row">
				<span class="label">Servers</span>
				<span class="value">{serverCount} registered</span>
			</div>
		</div>
	{/snippet}

	{#snippet footer()}
		{#if gateway.status?.registeredServers && gateway.status.registeredServers.length > 0}
			<div class="servers">
				{#each gateway.status.registeredServers.slice(0, 3) as server}
					<span class="server-badge" class:ready={server.ready}>
						{server.name}
					</span>
				{/each}
				{#if gateway.status.registeredServers.length > 3}
					<span class="more">+{gateway.status.registeredServers.length - 3}</span>
				{/if}
			</div>
		{/if}
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
		font-size: 0.75rem;
		max-width: 150px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.servers {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.server-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.7rem;
		font-family: var(--font-mono);
	}

	.server-badge.ready {
		background-color: rgba(34, 197, 94, 0.15);
		color: var(--color-success);
	}

	.more {
		color: var(--color-text-muted);
		font-size: 0.7rem;
	}
</style>
