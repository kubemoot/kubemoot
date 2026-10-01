<script lang="ts">
	import { readinessStatus } from '$lib/resource-status';
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import { DetailPanel } from '$components/layout';
	import { Section, InfoRow, StatusBadge } from '$components/common';
	import type { MCPServer } from '$types/kubemoot.js';

	let server = $state<MCPServer | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived($page.params.name as string);
	const ns = $derived($page.url.searchParams.get('namespace') || $namespace);

	async function fetchServer() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/mcpservers/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('MCP server not found');
			server = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch MCP server';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchServer();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchServer();
	});

	const status = $derived(readinessStatus(server?.status));
</script>

<DetailPanel title={name} subtitle="MCPServer" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">MCPServer</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if server}
				<StatusBadge {status} label={server.status?.phase || 'Unknown'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if server}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Image" value={server.spec.image} mono />
					<InfoRow label="Transport" value={server.spec.transport || 'http'} />
					<InfoRow label="Port" value={server.spec.port || 3000} />
					<InfoRow label="Replicas" value={server.spec.replicas || 2} />
					<InfoRow label="Service Account" value={server.spec.serviceAccountName} />
				</Section>

				<Section title="Status">
					<InfoRow label="Ready" value={server.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="Phase" value={server.status?.phase} />
					<InfoRow label="Endpoint" value={server.status?.endpoint} mono />
					<InfoRow label="Replicas" value={`${server.status?.availableReplicas || 0}/${server.spec.replicas || 2}`} />
					<InfoRow label="Message" value={server.status?.message} />
				</Section>

				{#if server.status?.tools && server.status.tools.length > 0}
					<Section title="Tools ({server.status.tools.length})">
						<div class="tools-list">
							{#each server.status.tools as tool}
								<div class="tool">
									<span class="tool-name">{tool.name}</span>
									{#if tool.description}
										<span class="tool-desc">{tool.description}</span>
									{/if}
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				{#if server.status?.registeredWith && server.status.registeredWith.length > 0}
					<Section title="Registered Gateways">
						{#each server.status.registeredWith as reg}
							<InfoRow label={reg.gatewayName}>
								{#snippet children()}
									<StatusBadge
										status={reg.registered ? 'success' : 'error'}
										label={reg.registered ? 'Registered' : 'Failed'}
										size="sm"
									/>
								{/snippet}
							</InfoRow>
						{/each}
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={server.metadata.namespace} />
					<InfoRow label="UID" value={server.metadata.uid} mono />
					<InfoRow label="Created" value={server.metadata.creationTimestamp} />
				</Section>
			</div>
		{/if}
	{/snippet}
</DetailPanel>

<style>
	.header-content {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
	}

	.title-section {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.kind {
		font-size: 0.75rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.title {
		font-size: 1.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.sections {
		display: flex;
		flex-direction: column;
		gap: 1.5rem;
	}

	.tools-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.tool {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		padding: 0.5rem;
		background-color: var(--color-bg-tertiary);
		border-radius: 0.25rem;
	}

	.tool-name {
		font-family: var(--font-mono);
		font-size: 0.85rem;
		font-weight: 500;
		color: var(--color-text);
	}

	.tool-desc {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
