<script lang="ts">
	import { phaseStatus } from '#lib/resource-status.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { MCPCatalog } from '#lib/types/kubemoot.js';

	let catalog = $state<MCPCatalog | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchCatalog() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/mcpcatalogs/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('MCP catalog not found');
			catalog = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch MCP catalog';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchCatalog();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchCatalog();
	});

	const status = $derived(phaseStatus(catalog?.status?.phase));
</script>

<DetailPanel title={name} subtitle="MCPCatalog" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">MCPCatalog</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if catalog}
				<StatusBadge {status} label={catalog.status?.phase || 'Unknown'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if catalog}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Type" value={catalog.spec.type} />
					<InfoRow label="URL" value={catalog.spec.url} mono />
					<InfoRow label="Sync Interval" value={catalog.spec.syncInterval || '24h'} />
					<InfoRow label="Max Servers" value={catalog.spec.maxServers || 100} />
					{#if catalog.spec.qualityPolicyRef}
						<InfoRow label="Quality Policy" value={catalog.spec.qualityPolicyRef} mono />
					{/if}
					{#if catalog.spec.agentRef}
						<InfoRow label="Agent Ref" value={catalog.spec.agentRef} mono />
					{/if}
				</Section>

				{#if catalog.spec.queries && catalog.spec.queries.length > 0}
					<Section title="Queries">
						<div class="tags">
							{#each catalog.spec.queries as query}
								<span class="tag">{query}</span>
							{/each}
						</div>
					</Section>
				{/if}

				<Section title="Status">
					<InfoRow label="Phase" value={catalog.status?.phase} />
					<InfoRow label="Servers Discovered" value={catalog.status?.serversDiscovered ?? 0} />
					<InfoRow label="Servers Allowed" value={catalog.status?.serversAllowed ?? 0} />
					<InfoRow label="Servers Blocked" value={catalog.status?.serversBlocked ?? 0} />
					<InfoRow label="Last Sync" value={catalog.status?.lastSync} />
					<InfoRow label="Next Sync" value={catalog.status?.nextSync} />
					<InfoRow label="Message" value={catalog.status?.message} />
				</Section>

				{#if catalog.status?.discoveredServers && catalog.status.discoveredServers.length > 0}
					<Section title="Discovered Servers ({catalog.status.discoveredServers.length})">
						<div class="servers-list">
							{#each catalog.status.discoveredServers as server}
								<div class="server" class:allowed={server.qualityDecision === 'allow'} class:blocked={server.qualityDecision === 'block'}>
									<div class="server-header">
										<span class="server-name">{server.name}</span>
										{#if server.qualityDecision}
											<StatusBadge
												status={server.qualityDecision === 'allow' ? 'success' : 'error'}
												label={server.qualityDecision}
												size="sm"
											/>
										{/if}
									</div>
									{#if server.description}
										<span class="server-description">{server.description}</span>
									{/if}
									<div class="server-meta">
										{#if server.author}<span>by {server.author}</span>{/if}
										{#if server.version}<span>v{server.version}</span>{/if}
										{#if server.transport}<span>{server.transport}</span>{/if}
									</div>
									{#if server.categories && server.categories.length > 0}
										<div class="server-categories">
											{#each server.categories as cat}
												<span class="cat-tag">{cat}</span>
											{/each}
										</div>
									{/if}
									{#if server.qualityReason}
										<span class="server-reason">{server.qualityReason}</span>
									{/if}
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={catalog.metadata.namespace} />
					<InfoRow label="UID" value={catalog.metadata.uid} mono />
					<InfoRow label="Created" value={catalog.metadata.creationTimestamp} />
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

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.tag {
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		padding: 0.25rem 0.625rem;
		border-radius: 0.375rem;
		font-size: 0.8rem;
		font-family: var(--font-mono);
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
		border-left: 3px solid var(--color-text-muted);
	}

	.server.allowed {
		border-left-color: var(--color-success);
	}

	.server.blocked {
		border-left-color: var(--color-error);
	}

	.server-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.server-name {
		font-weight: 500;
		color: var(--color-text);
		font-family: var(--font-mono);
		font-size: 0.85rem;
	}

	.server-description {
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}

	.server-meta {
		display: flex;
		gap: 0.75rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.server-categories {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}

	.cat-tag {
		background-color: var(--color-bg-secondary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		font-size: 0.65rem;
		color: var(--color-text-muted);
	}

	.server-reason {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		font-style: italic;
		margin-top: 0.25rem;
	}
</style>
