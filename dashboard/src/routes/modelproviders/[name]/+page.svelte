<script lang="ts">
	import { readinessStatus } from '#lib/resource-status.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { ModelProvider } from '#lib/types/kubemoot.js';

	let provider = $state<ModelProvider | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchProvider() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/modelproviders/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('Provider not found');
			provider = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch provider';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchProvider();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchProvider();
	});

	const status = $derived(readinessStatus(provider?.status, 'Failed'));

	const capacity = $derived(provider?.status?.capacity);
	const loadedModels = $derived(capacity?.loadedModels ?? []);
	const loadedNames = $derived(new Set(loadedModels.map((m) => m.name)));
	const cachedOnly = $derived(
		(capacity?.availableModels ?? [])
			.map((m) => (typeof m === 'string' ? m : m.name))
			.filter((n) => !loadedNames.has(n))
	);

	function formatMiB(mib: number | undefined): string {
		if (!mib || mib <= 0) return '-';
		if (mib >= 1024) return `${(mib / 1024).toFixed(1)} GiB`;
		return `${mib} MiB`;
	}

	function formatBytesMiB(bytes: number | undefined): string {
		if (!bytes || bytes <= 0) return '-';
		const mib = bytes / (1024 * 1024);
		if (mib >= 1024) return `${(mib / 1024).toFixed(1)} GiB`;
		return `${mib.toFixed(0)} MiB`;
	}

	function modelDetailsHref(modelName: string): string {
		if (!provider) return '#';
		const pns = encodeURIComponent(provider.metadata.namespace ?? 'kubemoot');
		return `${resolve('/models')}?model=${encodeURIComponent(modelName)}&provider=${encodeURIComponent(provider.metadata.name)}&namespace=${pns}`;
	}
</script>

<DetailPanel title={name} subtitle="Model Provider" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">Model Provider</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if provider}
				<StatusBadge {status} label={provider.status?.phase || 'Unknown'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if provider}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Type" value={provider.spec.type} />
					<InfoRow label="Endpoint" value={provider.spec.endpoint} mono />
					<InfoRow label="Secret Ref" value={provider.spec.secretRef} mono />
				</Section>

				<Section title="Status">
					<InfoRow label="Ready" value={provider.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="Phase" value={provider.status?.phase} />
					<InfoRow label="Message" value={provider.status?.message} />
				</Section>

				{#if provider.status?.providerInfo}
					<Section title="Provider Info">
						<InfoRow label="Version" value={provider.status.providerInfo.version} />
						<InfoRow label="Last Checked" value={provider.status.providerInfo.lastChecked} />
						{#if provider.status.providerInfo.rateLimits}
							<InfoRow label="Requests/min" value={provider.status.providerInfo.rateLimits.requestsPerMinute} />
							<InfoRow label="Tokens/min" value={provider.status.providerInfo.rateLimits.tokensPerMinute} />
						{/if}
					</Section>
				{/if}

				{#if capacity}
					<Section title="Capacity">
						{#if capacity.nodeName}
							<InfoRow label="Node" value={capacity.nodeName} mono />
						{/if}
						{#if capacity.maxParallel !== undefined}
							<InfoRow label="Max Parallel" value={capacity.maxParallel} />
						{/if}
						{#if capacity.vramTotalMiB !== undefined}
							<InfoRow label="VRAM Used" value="{formatMiB(capacity.vramUsedMiB)} / {formatMiB(capacity.vramTotalMiB)}" />
						{:else if capacity.vramUsedMiB !== undefined}
							<InfoRow label="VRAM Used" value={formatMiB(capacity.vramUsedMiB)} />
						{/if}
						{#if capacity.lastProbed}
							<InfoRow label="Last Probed" value={capacity.lastProbed} />
						{/if}
					</Section>
				{/if}

				{#if loadedModels.length > 0}
					<Section title="Loaded in VRAM ({loadedModels.length})">
						<div class="model-list">
							{#each loadedModels as m (m.name)}
								<div class="model-row">
									<span class="model-state loaded" title="In VRAM"></span>
									<a class="model-name mono" href={modelDetailsHref(m.name)}>{m.name}</a>
									<span class="model-meta">{formatBytesMiB(m.sizeVram)}</span>
									{#if m.expiresAt}
										<span class="model-meta">expires {new Date(m.expiresAt).toLocaleTimeString()}</span>
									{/if}
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				{#if cachedOnly.length > 0}
					<Section title="Cached on disk ({cachedOnly.length})" collapsible defaultOpen={true}>
						<div class="model-list">
							{#each cachedOnly as n (n)}
								<div class="model-row">
									<span class="model-state cached" title="On disk"></span>
									<a class="model-name mono" href={modelDetailsHref(n)}>{n}</a>
								</div>
							{/each}
						</div>
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={provider.metadata.namespace} />
					<InfoRow label="UID" value={provider.metadata.uid} mono />
					<InfoRow label="Created" value={provider.metadata.creationTimestamp} />
					<InfoRow label="Resource Version" value={provider.metadata.resourceVersion} mono />
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

	.model-list {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}

	.model-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.85rem;
	}

	.model-state {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.model-state.loaded {
		background-color: var(--color-success, #10b981);
		box-shadow: 0 0 4px var(--color-success, #10b981);
	}

	.model-state.cached {
		background-color: var(--color-text-muted, #6b7280);
		opacity: 0.6;
	}

	.model-name {
		font-family: var(--font-mono);
		color: var(--color-primary, #60a5fa);
		text-decoration: none;
	}

	.model-name:hover {
		text-decoration: underline;
	}

	.model-meta {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
