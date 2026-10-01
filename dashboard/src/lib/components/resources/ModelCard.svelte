<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Model } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		model: Model;
		showNamespace?: boolean;
		onAction?: () => void;
	}

	let { model, showNamespace = false, onAction }: Props = $props();

	let busy = $state(false);
	let actionError = $state<string | null>(null);

	const stateToStatus = {
		Available: 'success',
		Loaded: 'success',
		Pulling: 'pending',
		Pending: 'pending',
		Error: 'error'
	} as const;

	const status = $derived(
		stateToStatus[model.status?.state as keyof typeof stateToStatus] || 'unknown'
	);

	const statusLabel = $derived(model.status?.state || 'Unknown');

	const deleteUrl = $derived(
		`${resolve('/api/kubemoot/modelproviders/[name]/delete', { name: encodeURIComponent(model.spec.providerRef) })}?namespace=${encodeURIComponent(model.metadata.namespace ?? 'kubemoot')}`
	);

	async function deleteFromDisk(event: Event) {
		event.preventDefault();
		event.stopPropagation();
		if (busy) return;
		const ok = confirm(
			`Delete model "${model.spec.model}" from disk on provider "${model.spec.providerRef}"?\n\nThis is destructive - re-pulling can take many minutes. Other Model CRs that reference the same underlying model will also be affected.`
		);
		if (!ok) return;
		busy = true;
		actionError = null;
		try {
			const res = await fetch(deleteUrl, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ model: model.spec.model, confirm: true })
			});
			if (!res.ok) {
				const body = await res.text();
				throw new Error(body || `delete failed (HTTP ${res.status})`);
			}
			onAction?.();
		} catch (e) {
			actionError = e instanceof Error ? e.message : 'delete failed';
		} finally {
			busy = false;
		}
	}
</script>

<ResourceCard
	name={model.metadata.name}
	kind="Model"
	href="{resolve('/models/[name]', { name: model.metadata.name })}?namespace={model.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? model.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Model ID</span>
				<span class="value mono">{model.spec.model}</span>
			</div>
			<div class="row">
				<span class="label">Provider</span>
				<span class="value">{model.spec.providerRef}</span>
			</div>
			{#if model.status?.modelInfo?.size}
				<div class="row">
					<span class="label">Size</span>
					<span class="value">{model.status.modelInfo.size}</span>
				</div>
			{/if}
			{#if model.status?.modelInfo?.parameters}
				<div class="row">
					<span class="label">Parameters</span>
					<span class="value">{model.status.modelInfo.parameters}</span>
				</div>
			{/if}
			{#if actionError}
				<div class="error-row" role="alert">{actionError}</div>
			{/if}
		</div>
	{/snippet}

	{#snippet footer()}
		<button
			class="action-btn delete"
			type="button"
			title="Delete from disk on the underlying provider (destructive - requires re-pull)"
			disabled={busy}
			onclick={deleteFromDisk}
		>
			{busy ? 'Deleting…' : 'Delete from disk'}
		</button>
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

	.action-btn {
		font-size: 0.7rem;
		padding: 0.2rem 0.6rem;
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

	.error-row {
		font-size: 0.7rem;
		color: var(--color-error, #ef4444);
		padding: 0.25rem 0.5rem;
		background-color: rgba(239, 68, 68, 0.1);
		border-radius: 0.25rem;
		word-break: break-word;
	}
</style>
