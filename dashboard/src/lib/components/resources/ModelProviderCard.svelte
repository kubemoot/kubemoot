<script lang="ts">
	import { PENDING_TTL_MS, unsettledActions, type PendingAction } from '#lib/pending-actions.js';
	import { readinessStatus } from '#lib/resource-status.js';
	import { resolve } from '$app/paths';
	import type { ModelProvider, ModelProviderLoadedModel } from '#lib/types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';
	import { readOnly } from '#lib/stores/mode.js';

	interface Props {
		provider: ModelProvider;
		showNamespace?: boolean;
		onAction?: () => void;
	}

	let { provider, showNamespace = false, onAction }: Props = $props();

	let actionError = $state<string | null>(null);
	let now = $state(Date.now());

	// Pending actions are state-driven and persisted in localStorage so a
	// spinner that's mid-flight survives a page refresh. The truth source
	// is `ModelProvider.status.capacity.loadedModels`; a pending action
	// expires either when the CR catches up (loaded/unloaded as intended)
	// or after PENDING_TTL_MS (give-up timeout - operator probe never
	// confirmed, treat as failed).

	let pendingActions = $state<Record<string, PendingAction>>({});

	const storageKey = $derived(
		`kubemoot-pending-actions:${provider.metadata.namespace ?? 'kubemoot'}/${provider.metadata.name}`
	);

	// Hydrate from localStorage on first run (and prune stale entries).
	$effect(() => {
		if (typeof localStorage === 'undefined') return;
		try {
			const raw = localStorage.getItem(storageKey);
			if (!raw) return;
			const parsed = JSON.parse(raw) as Record<string, PendingAction>;
			const cutoff = Date.now() - PENDING_TTL_MS;
			const pruned: Record<string, PendingAction> = {};
			for (const [k, v] of Object.entries(parsed)) {
				if (v && v.startedAt > cutoff) pruned[k] = v;
			}
			pendingActions = pruned;
		} catch {
			// ignore malformed entries
		}
	});

	// Persist pendingActions to localStorage on every change.
	$effect(() => {
		if (typeof localStorage === 'undefined') return;
		try {
			if (Object.keys(pendingActions).length === 0) {
				localStorage.removeItem(storageKey);
			} else {
				localStorage.setItem(storageKey, JSON.stringify(pendingActions));
			}
		} catch {
			// ignore quota / privacy errors
		}
	});

	$effect(() => {
		const tick = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(tick);
	});

	// Reconcile pendingActions with the CR's actual state.
	// - load completes: actuallyLoaded.has(model) → drop pending
	// - unload completes: !actuallyLoaded.has(model) → drop pending
	// - TTL expires: drop pending (give up on the spinner)
	$effect(() => {
		const actuallyLoaded = new Set(
			(provider.status?.capacity?.loadedModels ?? []).map((m) => m.name)
		);
		const next = unsettledActions(pendingActions, actuallyLoaded, Date.now() - PENDING_TTL_MS);
		if (next) pendingActions = next;
	});

	function isPending(modelName: string, action?: 'load' | 'unload'): boolean {
		const p = pendingActions[modelName];
		if (!p) return false;
		if (action && p.action !== action) return false;
		return true;
	}

	const status = $derived(readinessStatus(provider.status, 'Failed'));

	const statusLabel = $derived(provider.status?.phase || 'Unknown');
	const capacity = $derived(provider.status?.capacity);
	const weight = $derived(provider.spec.scheduling?.weight);

	// CR truth: what's actually loaded right now per the operator's latest
	// capacity probe. This is the only source of state for which button
	// is enabled. The pendingActions set drives which buttons show the
	// spinner - independent of state.
	const baseLoadedModels = $derived<ModelProviderLoadedModel[]>(capacity?.loadedModels ?? []);
	const baseLoadedByName = $derived(
		new Map(baseLoadedModels.map((m) => [m.name, m] as const))
	);
	// availableModels carries {name,sizeBytes}; reduce to names here so the rest of
	// the card is unchanged. Tolerate the old string[] shape during rollout skew.
	const allAvailable = $derived(
		(capacity?.availableModels ?? []).map((m) => (typeof m === 'string' ? m : m.name))
	);

	// Unified, deduped, alphabetically-sorted list of every model this provider
	// knows about (loaded + cached). Each entry carries its current load state
	// so the row can render the right indicator + button enable.
	interface ModelEntry {
		name: string;
		loadedInfo: ModelProviderLoadedModel | undefined; // undefined → cached only
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

	const vramUsed = $derived(capacity?.vramUsedMiB ?? 0);
	const vramTotal = $derived(capacity?.vramTotalMiB ?? 0);
	const vramPct = $derived(vramTotal > 0 ? Math.min(100, Math.round((vramUsed * 100) / vramTotal)) : 0);

	const apiBase = $derived(
		`${resolve('/api/kubemoot/modelproviders/[name]', { name: provider.metadata.name })}?namespace=${encodeURIComponent(provider.metadata.namespace ?? 'kubemoot')}`
	);
	const loadUrl = $derived(apiBase.replace('?', '/load?'));
	const unloadUrl = $derived(apiBase.replace('?', '/unload?'));

	function actionUrl(action: 'load' | 'unload'): string {
		return action === 'load' ? loadUrl : unloadUrl;
	}

	function modelDetailsHref(modelName: string): string {
		const ns = encodeURIComponent(provider.metadata.namespace ?? 'kubemoot');
		return `${resolve('/models')}?model=${encodeURIComponent(modelName)}&provider=${encodeURIComponent(provider.metadata.name)}&namespace=${ns}`;
	}

	async function runAction(model: string, action: 'load' | 'unload') {
		if (isPending(model)) return;
		actionError = null;
		// Record the pending action BEFORE the HTTP call - that way a refresh
		// mid-flight still shows the spinner because localStorage is updated
		// the moment the user clicks.
		pendingActions = { ...pendingActions, [model]: { action, startedAt: Date.now() } };
		try {
			const res = await fetch(actionUrl(action), {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ model })
			});
			if (!res.ok) {
				const body = await res.text();
				throw new Error(body || `${action} failed (HTTP ${res.status})`);
			}
			// HTTP returned success. Don't clear the pending action here -
			// the reconcile $effect drops it once the CR confirms the new
			// state (or after TTL). Schedule refetches so the CR catches up
			// faster than the operator's natural probe interval.
			onAction?.();
			scheduleRefetches();
		} catch (e) {
			actionError = e instanceof Error ? e.message : `${action} failed`;
			// Drop the pending action on failure - spinner shouldn't spin
			// forever on a server error. (TTL would also catch this but we
			// can clear immediately on a known failure.)
			const next = { ...pendingActions };
			delete next[model];
			pendingActions = next;
		}
	}

	function stopProp(e: Event) {
		e.stopPropagation();
	}

	function scheduleRefetches() {
		// Operator capacity probe runs on an interval. Poll at growing
		// offsets to catch the moment the CR catches up with reality.
		for (const ms of [3000, 8000, 18000, 35000]) {
			setTimeout(() => onAction?.(), ms);
		}
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

	function formatMiB(bytes: number | undefined): string {
		if (!bytes || bytes <= 0) return '-';
		const mib = bytes / (1024 * 1024);
		if (mib >= 1024) return `${(mib / 1024).toFixed(1)} GiB`;
		return `${mib.toFixed(0)} MiB`;
	}

	function formatMiBFromMiB(mib: number | undefined): string {
		if (!mib || mib <= 0) return '-';
		if (mib >= 1024) return `${(mib / 1024).toFixed(1)} GiB`;
		return `${mib} MiB`;
	}
</script>

<ResourceCard
	name={provider.metadata.name}
	kind="Model Provider"
	href="{resolve('/modelproviders/[name]', { name: provider.metadata.name })}?namespace={provider.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? provider.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Type</span>
				<span class="value type-badge">{provider.spec.type}</span>
			</div>
			{#if weight !== undefined}
				<div class="row">
					<span class="label">Weight</span>
					<span class="value">{weight}</span>
				</div>
			{/if}
			{#if provider.spec.endpoint}
				<div class="row">
					<span class="label">Endpoint</span>
					<span class="value mono">{provider.spec.endpoint}</span>
				</div>
			{/if}
			{#if provider.status?.providerInfo?.version}
				<div class="row">
					<span class="label">Version</span>
					<span class="value">{provider.status.providerInfo.version}</span>
				</div>
			{/if}
			{#if capacity?.nodeName}
				<div class="row">
					<span class="label">Node</span>
					<span class="value mono">{capacity.nodeName}</span>
				</div>
			{/if}
			{#if vramTotal > 0}
				<div class="row">
					<span class="label">VRAM</span>
					<span class="value">{formatMiBFromMiB(vramUsed)} / {formatMiBFromMiB(vramTotal)}</span>
				</div>
				<div class="vram-bar" aria-hidden="true">
					<div class="vram-fill" style="width: {vramPct}%"></div>
				</div>
			{:else if vramUsed > 0}
				<div class="row">
					<span class="label">VRAM used</span>
					<span class="value">{formatMiBFromMiB(vramUsed)}</span>
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
							{@const loadingPending = isPending(entry.name, 'load')}
							{@const unloadingPending = isPending(entry.name, 'unload')}
							{@const pending = loadingPending || unloadingPending}
							<!-- Hover-reveal action: the row is quiet at rest. Only one action
							     exists per row -- Unload for loaded, Load for cached. When a
							     pending action is in flight we force the spinner visible
							     (not hover-gated) so the user sees what's happening. -->
							<div class="model-row" class:row-loaded={loaded} class:row-pending={pending}>
								<div class="model-info">
									<span
										class="model-state"
										class:loaded={loaded}
										class:cached={!loaded}
										title={loaded ? 'In VRAM' : 'On disk, not in VRAM'}
									></span>
									<a
										class="model-name mono"
										href={modelDetailsHref(entry.name)}
										title="View Model CRs for {entry.name}"
										onclick={stopProp}
									>{entry.name}</a>
									{#if entry.loadedInfo?.sizeVram}
										<span class="model-meta">{formatMiB(entry.loadedInfo.sizeVram)}</span>
									{/if}
									{#if entry.loadedInfo?.expiresAt}
										<span class="model-meta ttl">TTL {formatRemaining(entry.loadedInfo.expiresAt, now)}</span>
									{/if}
								</div>
								{#if !$readOnly}
								<div class="action-group" class:show={pending}>
									{#if loaded}
										<button
											class="action-btn unload"
											class:busy={unloadingPending}
											title={unloadingPending
												? 'Unloading… waiting for capacity probe to confirm'
												: 'Evict from VRAM (model stays on disk)'}
											disabled={pending}
											onclick={(e) => {
												e.preventDefault();
												runAction(entry.name, 'unload');
											}}
										>
											{#if unloadingPending}
												<span class="spinner" aria-hidden="true"></span>
												<span>Unloading…</span>
											{:else}
												Unload
											{/if}
										</button>
									{:else}
										<button
											class="action-btn load"
											class:busy={loadingPending}
											title={loadingPending
												? 'Loading… waiting for capacity probe to confirm'
												: 'Load into VRAM'}
											disabled={pending}
											onclick={(e) => {
												e.preventDefault();
												runAction(entry.name, 'load');
											}}
										>
											{#if loadingPending}
												<span class="spinner" aria-hidden="true"></span>
												<span>Loading…</span>
											{:else}
												Load
											{/if}
										</button>
									{/if}
								</div>
								{/if}
							</div>
						{/each}
					</div>
				</div>
			{/if}

			{#if actionError}
				<div class="error-row" role="alert">{actionError}</div>
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

	.action-group {
		display: flex;
		gap: 0.25rem;
		flex-shrink: 0;
		/* Hover-reveal: invisible at rest, fades in on row hover or when
		   a pending action is in flight (.show is set programmatically). */
		opacity: 0;
		transition: opacity 0.12s ease;
		pointer-events: none;
	}

	.model-row:hover .action-group,
	.model-row:focus-within .action-group,
	.action-group.show {
		opacity: 1;
		pointer-events: auto;
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

	.model-row.row-pending {
		/* When a pending action is in flight, the row itself shows a subtle
		   busy indicator background so the user can scan a long list and
		   spot which row is currently changing state. */
		background-color: rgba(59, 130, 246, 0.07);
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

	.action-btn {
		font-size: 0.7rem;
		padding: 0.15rem 0.5rem;
		border-radius: 0.25rem;
		border: 1px solid var(--color-border, rgba(255, 255, 255, 0.12));
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		cursor: pointer;
		min-width: 56px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.35rem;
		transition: background-color 0.15s ease, border-color 0.15s ease;
	}

	.action-btn.busy {
		opacity: 0.85;
		cursor: progress;
	}

	.spinner {
		display: inline-block;
		width: 0.7rem;
		height: 0.7rem;
		border: 1.5px solid currentColor;
		border-right-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.action-btn:hover:not(:disabled) {
		background-color: var(--color-bg-secondary, rgba(255, 255, 255, 0.08));
	}

	.action-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.action-btn.unload {
		border-color: rgba(239, 68, 68, 0.3);
		color: #fca5a5;
	}

	.action-btn.unload:hover:not(:disabled) {
		background-color: rgba(239, 68, 68, 0.1);
	}

	.action-btn.load {
		border-color: rgba(16, 185, 129, 0.3);
		color: #6ee7b7;
	}

	.action-btn.load:hover:not(:disabled) {
		background-color: rgba(16, 185, 129, 0.1);
	}

	.error-row {
		font-size: 0.7rem;
		color: var(--color-error, #ef4444);
		padding: 0.25rem 0.5rem;
		background-color: rgba(239, 68, 68, 0.1);
		border-radius: 0.25rem;
		word-break: break-word;
	}
</style>
