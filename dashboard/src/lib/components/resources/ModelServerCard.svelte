<script lang="ts">
	import { formatBytes, formatMiB } from '#lib/format.js';
	import { acceleratorMemoryTotalMiB, acceleratorMemoryUsedMiB, loadedAcceleratorMemory } from '#lib/accelerator-capacity.js';
	import { readinessStatus } from '#lib/resource-status.js';
	import { resolve } from '$app/paths';
	import type { ModelServer, ModelServerLoadedModel } from '#lib/types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';
	import UsageBar from '../common/UsageBar.svelte';
	import type { ServerAccelerator } from '#lib/accelerator-status.js';

	interface Props {
		server: ModelServer;
		showNamespace?: boolean;
		/** The accelerator behind the server and how it is working, when the page could tell. */
		accelerator?: ServerAccelerator;
	}

	let { server, showNamespace = false, accelerator }: Props = $props();

	let now = $state(Date.now());

	$effect(() => {
		const tick = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(tick);
	});

	const status = $derived(readinessStatus(server.status, 'Failed'));

	const statusLabel = $derived(server.status?.phase || 'Unknown');
	const capacity = $derived(server.status?.capacity);
	const weight = $derived(server.spec.scheduling?.weight);

	// What is loaded right now, per the operator's latest capacity probe.
	const baseLoadedModels = $derived<ModelServerLoadedModel[]>(capacity?.loadedModels ?? []);
	const baseLoadedByName = $derived(
		new Map(baseLoadedModels.map((m) => [m.name, m] as const))
	);
	// availableModels carries {name,sizeBytes}; reduce to names here so the rest of
	// the card is unchanged. Tolerate the old string[] shape during rollout skew.
	const allAvailable = $derived(
		(capacity?.availableModels ?? []).map((m) => (typeof m === 'string' ? m : m.name))
	);

	// Unified, deduped, alphabetically-sorted list of every model this server
	// knows about (loaded + cached). Each entry carries its current load state
	// so the row can render the right indicator.
	interface ModelEntry {
		name: string;
		loadedInfo: ModelServerLoadedModel | undefined; // undefined → cached only
	}
	const modelEntries = $derived.by<ModelEntry[]>(() => {
		const names = new Set<string>();
		for (const m of baseLoadedModels) names.add(m.name);
		for (const n of allAvailable) names.add(n);
		return [...names]
			.sort((a, b) => a.localeCompare(b))
			.map((name) => ({ name, loadedInfo: baseLoadedByName.get(name) }));
	});

	function isLoaded(modelName: string): boolean {
		return baseLoadedByName.has(modelName);
	}

	const vramUsed = $derived(acceleratorMemoryUsedMiB(capacity) ?? 0);
	const vramTotal = $derived(acceleratorMemoryTotalMiB(capacity) ?? 0);
	const vramPct = $derived(vramTotal > 0 ? Math.min(100, Math.round((vramUsed * 100) / vramTotal)) : 0);

	function modelDetailsHref(modelName: string): string {
		const ns = encodeURIComponent(server.metadata.namespace ?? 'kubemoot');
		return `${resolve('/models')}?model=${encodeURIComponent(modelName)}&server=${encodeURIComponent(server.metadata.name)}&namespace=${ns}`;
	}

	function stopProp(e: Event) {
		e.stopPropagation();
	}

	function formatRemaining(expiresAt: string | undefined, nowMs: number): string {
		if (!expiresAt) return '';
		const diff = new Date(expiresAt).getTime() - nowMs;
		if (diff <= 0) return 'expiring';
		const mins = Math.floor(diff / 60_000);
		const secs = Math.floor((diff % 60_000) / 1000);
		if (mins >= 60) return `${Math.floor(mins / 60)}h ${mins % 60}m`;
		if (mins > 0) return `${mins}m ${secs}s`;
		return `${secs}s`;
	}

</script>

<ResourceCard
	name={server.metadata.name}
	kind="Model Server"
	href="{resolve('/modelservers/[name]', { name: server.metadata.name })}?namespace={server.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? server.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Type</span>
				<span class="value type-badge">{server.spec.type}</span>
			</div>
			{#if weight !== undefined}
				<div class="row">
					<span class="label">Weight</span>
					<span class="value">{weight}</span>
				</div>
			{/if}
			{#if server.spec.endpoint}
				<div class="row">
					<span class="label">Endpoint</span>
					<span class="value mono">{server.spec.endpoint}</span>
				</div>
			{/if}
			{#if server.status?.serverInfo?.version}
				<div class="row">
					<span class="label">Version</span>
					<span class="value">{server.status.serverInfo.version}</span>
				</div>
			{/if}
			{#if capacity?.nodeName}
				<div class="row">
					<span class="label">Node</span>
					<span class="value mono">{capacity.nodeName}</span>
				</div>
			{/if}
			{#if accelerator}
				<div class="row" data-testid="server-accelerator">
					<span class="label">Accelerator</span>
					<span class="value">{accelerator.label}</span>
				</div>
				{#if !capacity?.nodeName}
					<div class="row">
						<span class="label">Node</span>
						<span class="value mono">{accelerator.node}</span>
					</div>
				{/if}
				{#if accelerator.unavailable}
					<p class="unavailable">{accelerator.unavailable}</p>
				{:else}
					{#each [accelerator.busy, accelerator.memory].filter((b) => b !== undefined) as b (b.label)}
						<UsageBar label={b.label} text={b.text} fraction={b.fraction} level={b.level} />
					{/each}
				{/if}
			{/if}

			{#if vramTotal > 0}
				<div class="row">
					<span class="label">Accelerator memory</span>
					<span class="value">{formatMiB(vramUsed)} / {formatMiB(vramTotal)}</span>
				</div>
				<div class="vram-bar" aria-hidden="true">
					<div class="vram-fill" style="width: {vramPct}%"></div>
				</div>
			{:else if vramUsed > 0}
				<div class="row">
					<span class="label">Accelerator memory used</span>
					<span class="value">{formatMiB(vramUsed)}</span>
				</div>
			{/if}

			{#if modelEntries.length > 0}
				<div class="models">
					<div class="models-section">
						<div class="section-title">
							Models <span class="muted">({baseLoadedModels.length} / {modelEntries.length} loaded)</span>
						</div>
						{#each modelEntries as entry (entry.name)}
							{@const loaded = isLoaded(entry.name)}
							<div class="model-row" class:row-loaded={loaded}>
								<div class="model-info">
									<span
										class="model-state"
										class:loaded={loaded}
										class:cached={!loaded}
										title={loaded ? 'In accelerator memory' : 'On disk, not in accelerator memory'}
									></span>
									<a
										class="model-name mono"
										href={modelDetailsHref(entry.name)}
										onclick={stopProp}
									>{entry.name}</a>
									{#if loadedAcceleratorMemory(entry.loadedInfo)}
										<span class="model-meta">{formatBytes(loadedAcceleratorMemory(entry.loadedInfo))}</span>
									{/if}
									{#if entry.loadedInfo?.expiresAt}
										<span class="model-meta ttl">TTL {formatRemaining(entry.loadedInfo.expiresAt, now)}</span>
									{/if}
								</div>
							</div>
						{/each}
					</div>
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

	.unavailable {
		margin: 0;
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}

	.value {
		color: var(--color-text);
	}

	.value.mono {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		max-width: 200px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.type-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.75rem;
		text-transform: capitalize;
	}

	.vram-bar {
		width: 100%;
		height: 4px;
		background-color: var(--color-bg-tertiary);
		border-radius: 2px;
		overflow: hidden;
	}

	.vram-fill {
		height: 100%;
		background-color: var(--color-success, #10b981);
		transition: width 0.4s ease;
	}

	.models {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-top: 0.25rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border, rgba(255, 255, 255, 0.06));
	}

	.models-section {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.section-title {
		font-size: 0.7rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.muted {
		font-weight: 400;
	}

	.model-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
		padding: 0.2rem 0.35rem;
		border-radius: 0.25rem;
		transition: background-color 0.12s ease;
	}

	.model-row:hover {
		background-color: rgba(255, 255, 255, 0.03);
	}

	/* Loaded row gets a quiet visual emphasis so it's distinguishable at a
	   glance from the cached rows -- without needing a "Loaded in VRAM"
	   section header. */
	.model-row.row-loaded {
		background-color: rgba(16, 185, 129, 0.04);
	}

	.model-row.row-loaded:hover {
		background-color: rgba(16, 185, 129, 0.08);
	}

	.model-info {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		flex: 1;
		min-width: 0;
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
		font-size: 0.75rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		flex: 1;
		min-width: 0;
		color: var(--color-primary, #60a5fa);
		text-decoration: none;
	}

	.model-name:hover {
		text-decoration: underline;
	}

	.model-meta {
		color: var(--color-text-muted);
		font-size: 0.7rem;
		flex-shrink: 0;
	}

	.model-meta.ttl {
		font-variant-numeric: tabular-nums;
	}
</style>
