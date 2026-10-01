<script lang="ts">
	import { readinessStatus } from '$lib/resource-status';
	import { base } from '$app/paths';
	import type { MCPServer } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		server: MCPServer;
		showNamespace?: boolean;
	}

	let { server, showNamespace = false }: Props = $props();

	const status = $derived(readinessStatus(server.status));

	const statusLabel = $derived(server.status?.phase || 'Unknown');
	const toolCount = $derived(server.status?.tools?.length || 0);
</script>

<ResourceCard
	name={server.metadata.name}
	kind="MCPServer"
	href="{base}/mcpservers/{server.metadata.name}?namespace={server.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? server.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Transport</span>
				<span class="value transport">{server.spec.transport || 'http'}</span>
			</div>
			{#if server.spec.image}
				<div class="row">
					<span class="label">Image</span>
					<span class="value mono" title={server.spec.image}>
						{server.spec.image.split('/').pop()?.split(':')[0] || server.spec.image}
					</span>
				</div>
			{/if}
			{#if server.status?.endpoint}
				<div class="row">
					<span class="label">Endpoint</span>
					<span class="value mono">{server.status.endpoint}</span>
				</div>
			{/if}
			<div class="row">
				<span class="label">Replicas</span>
				<span class="value">{server.status?.availableReplicas || 0}/{server.spec.replicas || 2}</span>
			</div>
		</div>
	{/snippet}

	{#snippet footer()}
		<div class="tools">
			<span class="tool-count">{toolCount} tool{toolCount !== 1 ? 's' : ''}</span>
			{#if server.status?.tools && server.status.tools.length > 0}
				<span class="tool-preview">
					{server.status.tools.slice(0, 3).map(t => t.name).join(', ')}
					{server.status.tools.length > 3 ? '...' : ''}
				</span>
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
		font-size: 0.75rem;
		max-width: 150px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.transport {
		text-transform: uppercase;
		font-size: 0.7rem;
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.tools {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.tool-count {
		font-weight: 500;
		color: var(--color-text);
	}

	.tool-preview {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
	}
</style>
