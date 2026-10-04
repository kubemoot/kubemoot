<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, readOnly, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { Model } from '#lib/types/kubemoot.js';

	let model = $state<Model | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let busy = $state(false);
	let actionError = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchModel() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/models/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('Model not found');
			model = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch model';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchModel();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchModel();
	});

	const stateToStatus = {
		Available: 'success',
		Loaded: 'success',
		Pulling: 'pending',
		Pending: 'pending',
		Error: 'error'
	} as const;

	const status = $derived(
		stateToStatus[model?.status?.state as keyof typeof stateToStatus] || 'unknown'
	);

	const statusLabel = $derived(model?.status?.state || 'Unknown');

	async function deleteFromDisk() {
		if (!model || busy) return;
		const ok = confirm(
			`Delete model "${model.spec.model}" from disk on provider "${model.spec.providerRef}"?\n\nThis is destructive - re-pulling can take many minutes. Other Model CRs that reference the same underlying model will also be affected.`
		);
		if (!ok) return;
		busy = true;
		actionError = null;
		try {
			const url = `${resolve('/api/kubemoot/modelproviders/[name]/delete', { name: encodeURIComponent(model.spec.providerRef) })}?namespace=${encodeURIComponent(model.metadata.namespace ?? 'kubemoot')}`;
			const res = await fetch(url, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ model: model.spec.model, confirm: true })
			});
			if (!res.ok) {
				const body = await res.text();
				throw new Error(body || `delete failed (HTTP ${res.status})`);
			}
			fetchModel();
		} catch (e) {
			actionError = e instanceof Error ? e.message : 'delete failed';
		} finally {
			busy = false;
		}
	}
</script>

<DetailPanel title={name} subtitle="Model" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">Model</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if model}
				<div class="header-actions">
					<StatusBadge {status} label={statusLabel} />
					{#if !$readOnly}
						<button
							class="action-btn delete"
							type="button"
							title="Delete from disk on the underlying provider (destructive - requires re-pull)"
							disabled={busy}
							onclick={deleteFromDisk}
						>
							{busy ? 'Deleting…' : 'Delete from disk'}
						</button>
					{/if}
				</div>
			{/if}
		</div>
		{#if actionError}
			<div class="error-row" role="alert">{actionError}</div>
		{/if}
	{/snippet}

	{#snippet children()}
		{#if model}
			<div class="sections">
				<Section title="Spec">
					<InfoRow label="Model ID" value={model.spec.model} mono />
					<InfoRow label="Provider Ref" value={model.spec.providerRef} />
					<InfoRow label="Quantization" value={model.spec.quantization} />
					<InfoRow label="Context Length" value={model.spec.contextLength} />
				</Section>

				<Section title="Status">
					<InfoRow label="Ready" value={model.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="State" value={model.status?.state} />
					<InfoRow label="Endpoint" value={model.status?.endpoint} mono />
					<InfoRow label="Message" value={model.status?.message} />
				</Section>

				{#if model.status?.modelInfo}
					<Section title="Model Info">
						<InfoRow label="Size" value={model.status.modelInfo.size} />
						<InfoRow label="Parameters" value={model.status.modelInfo.parameters} />
						<InfoRow label="Family" value={model.status.modelInfo.family} />
						<InfoRow label="Format" value={model.status.modelInfo.format} />
						<InfoRow label="Quantization" value={model.status.modelInfo.quantization} />
						<InfoRow label="Context Length" value={model.status.modelInfo.contextLength} />
						<InfoRow label="Digest" value={model.status.modelInfo.digest} mono />
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={model.metadata.namespace} />
					<InfoRow label="UID" value={model.metadata.uid} mono />
					<InfoRow label="Created" value={model.metadata.creationTimestamp} />
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

	.header-actions {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.action-btn {
		font-size: 0.75rem;
		padding: 0.3rem 0.7rem;
		border-radius: 0.25rem;
		border: 1px solid rgba(239, 68, 68, 0.3);
		background-color: var(--color-bg-tertiary);
		color: #fca5a5;
		cursor: pointer;
	}

	.action-btn:hover:not(:disabled) {
		background-color: rgba(239, 68, 68, 0.1);
	}

	.action-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
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

	.error-row {
		margin-top: 0.5rem;
		font-size: 0.75rem;
		color: var(--color-error, #ef4444);
		padding: 0.4rem 0.6rem;
		background-color: rgba(239, 68, 68, 0.1);
		border-radius: 0.25rem;
		word-break: break-word;
	}
</style>
