<script lang="ts">
	import { readinessStatus } from '#lib/resource-status.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { ModelProvider } from '#lib/types/kubemoot.js';
	import { calibrationBadge, calibrationRows, hasCalibration } from '#lib/provider-calibration.js';
	import { formatBytes, formatMiB } from '#lib/format.js';
	import { formatTimestamp } from '#lib/discussion-timing.js';

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
	const calibration = $derived(provider?.status?.calibration);
	const calibrationTable = $derived(calibrationRows(calibration));
	const loadedModels = $derived(capacity?.loadedModels ?? []);
	const loadedNames = $derived(new Set(loadedModels.map((m) => m.name)));
	const cachedOnly = $derived(
		(capacity?.availableModels ?? [])
			.map((m) => (typeof m === 'string' ? m : m.name))
			.filter((n) => !loadedNames.has(n))
	);


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

				<Section title="Calibration">
					{#if hasCalibration(calibration)}
						<div class="calibration-head">
							<StatusBadge status={calibrationBadge(calibration.phase)} label={calibration.phase || 'Unknown'} />
							{#if calibration.message}<span class="model-meta">{calibration.message}</span>{/if}
						</div>
						<InfoRow label="Engine" value={calibration.engineVersion} />
						<InfoRow label="Completed" value={formatTimestamp(calibration.completedAt)} />
						{#if calibrationTable.length > 0}
							<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
							<div class="calibration-table" role="region" aria-label="Calibration per model" tabindex="0">
								<table>
									<caption class="sr-only">What calibration measured for each model on {name}</caption>
									<thead>
										<tr>
											<th scope="col">Model</th><th scope="col">Fits</th><th scope="col">Load</th>
											<th scope="col">Generate</th><th scope="col">Prompt</th>
											<th scope="col">Parallel</th><th scope="col">Memory</th>
										</tr>
									</thead>
									<tbody>
										{#each calibrationTable as row (row.model)}
											<tr>
												<td class="mono">
													{row.model}
													{#if row.fits && row.reason}<div class="model-meta">{row.reason}</div>{/if}
												</td>
												<td>{row.fits ? 'yes' : 'no'}</td>
												<td>{row.load}</td>
												{#if row.fits}
													<td>{row.generate}</td><td>{row.prompt}</td>
													<td>{row.parallel}</td><td>{row.resident}</td>
												{:else}
													<td colspan="4" class="model-meta">{row.reason}</td>
												{/if}
											</tr>
										{/each}
									</tbody>
								</table>
							</div>
						{/if}
					{:else}
						<span class="model-meta">Not calibrated yet.</span>
					{/if}
				</Section>

				{#if loadedModels.length > 0}
					<Section title="Loaded in VRAM ({loadedModels.length})">
						<div class="model-list">
							{#each loadedModels as m (m.name)}
								<div class="model-row">
									<span class="model-state loaded" title="In VRAM"></span>
									<a class="model-name mono" href={modelDetailsHref(m.name)}>{m.name}</a>
									<span class="model-meta">{formatBytes(m.sizeVram)}</span>
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

	.calibration-head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}

	.calibration-table {
		overflow-x: auto;
		margin-top: 0.5rem;
	}

	.calibration-table table {
		border-collapse: collapse;
		font-size: 0.8rem;
		font-variant-numeric: tabular-nums;
	}

	.calibration-table th,
	.calibration-table td {
		padding: 0.3rem 0.75rem 0.3rem 0;
		text-align: left;
		white-space: nowrap;
	}

	.calibration-table th {
		color: var(--color-text-muted);
		font-weight: 500;
	}

	.mono {
		font-family: var(--font-mono);
	}

	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin: -1px;
		overflow: hidden;
		clip: rect(0, 0, 0, 0);
		white-space: nowrap;
		border: 0;
	}
</style>
