<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { base } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import { DetailPanel } from '$components/layout';
	import { Section, InfoRow, StatusBadge } from '$components/common';
	import type { MCPGateway } from '$types/kubemoot.js';

	let gateway = $state<MCPGateway | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived($page.params.name);
	const ns = $derived($page.url.searchParams.get('namespace') || $namespace);

	async function fetchGateway() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${base}/api/kubemoot/mcpgateways/${name}?namespace=${ns}`);
			if (!res.ok) throw new Error('MCP gateway not found');
			gateway = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch MCP gateway';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchGateway();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchGateway();
	});

	const status = $derived(
		gateway?.status?.ready
			? 'success'
			: gateway?.status?.phase === 'Error'
				? 'error'
				: 'pending'
	);
</script>

<DetailPanel title={name} subtitle="MCPGateway" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">MCPGateway</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if gateway}
				<StatusBadge {status} label={gateway.status?.phase || 'Unknown'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if gateway}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Implementation" value={gateway.spec.implementation || 'kubemoot'} />
					<InfoRow label="Port" value={gateway.spec.port} />
					<InfoRow label="Replicas" value={gateway.spec.replicas} />
				</Section>

				<Section title="Status">
					<InfoRow label="Ready" value={gateway.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="Phase" value={gateway.status?.phase} />
					<InfoRow label="Endpoint" value={gateway.status?.endpoint} mono />
					<InfoRow label="Admin Endpoint" value={gateway.status?.adminEndpoint} mono />
					<InfoRow label="Message" value={gateway.status?.message} />
				</Section>

				{#if gateway.status?.registeredServers && gateway.status.registeredServers.length > 0}
					<Section title="Registered Servers ({gateway.status.registeredServers.length})">
						<div class="servers-list">
							{#each gateway.status.registeredServers as server}
								<div class="server" class:ready={server.ready}>
									<div class="server-header">
										<span class="server-name">{server.name}</span>
										<StatusBadge
											status={server.ready ? 'success' : 'error'}
											label={server.ready ? 'Ready' : 'Not Ready'}
											size="sm"
										/>
									</div>
									<span class="server-endpoint">{server.endpoint}</span>
									{#if server.toolCount !== undefined}
										<span class="server-tools">{server.toolCount} tools</span>
									{/if}
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={gateway.metadata.namespace} />
					<InfoRow label="UID" value={gateway.metadata.uid} mono />
					<InfoRow label="Created" value={gateway.metadata.creationTimestamp} />
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

	.servers-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.server {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.75rem;
		background-color: var(--color-bg-tertiary);
		border-radius: 0.375rem;
		border-left: 3px solid var(--color-error);
	}

	.server.ready {
		border-left-color: var(--color-success);
	}

	.server-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.server-name {
		font-weight: 500;
		color: var(--color-text);
	}

	.server-endpoint {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.server-tools {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
