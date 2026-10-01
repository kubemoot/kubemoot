<script lang="ts">
	import { readinessStatus } from '$lib/resource-status';
	import { base } from '$app/paths';
	import type { RAGSource } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		source: RAGSource;
		showNamespace?: boolean;
	}

	let { source, showNamespace = false }: Props = $props();

	const status = $derived(readinessStatus(source.status));

	const statusLabel = $derived(source.status?.phase || 'Unknown');
</script>

<ResourceCard
	name={source.metadata.name}
	kind="RAGSource"
	href="{base}/ragsources/{source.metadata.name}?namespace={source.metadata.namespace}"
	{status}
	{statusLabel}
	namespace={showNamespace ? source.metadata.namespace : undefined}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Source Type</span>
				<span class="value type-badge">{source.spec.source.type}</span>
			</div>
			<div class="row">
				<span class="label">Vector Store</span>
				<span class="value">{source.spec.vectorStore.type}</span>
			</div>
			{#if source.status?.indexStatus?.documentCount !== undefined}
				<div class="row">
					<span class="label">Documents</span>
					<span class="value">{source.status.indexStatus.documentCount}</span>
				</div>
			{/if}
			{#if source.status?.indexStatus?.chunkCount !== undefined}
				<div class="row">
					<span class="label">Chunks</span>
					<span class="value">{source.status.indexStatus.chunkCount}</span>
				</div>
			{/if}
			{#if source.status?.phase === 'Error' && source.status?.message}
				<div class="error-msg" title={source.status.message}>
					{source.status.message.length > 80 ? source.status.message.substring(0, 80) + '...' : source.status.message}
				</div>
			{/if}
		</div>
	{/snippet}

	{#snippet footer()}
		{#if source.spec.source.gitUrl}
			<div class="source-url" title={source.spec.source.gitUrl}>
				{source.spec.source.gitUrl.replace('https://github.com/', '')}
			</div>
		{:else if source.spec.source.webUrl}
			<div class="source-url" title={source.spec.source.webUrl}>
				{source.spec.source.webUrl}
			</div>
		{/if}
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

	.type-badge {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.75rem;
		text-transform: uppercase;
	}

	.error-msg {
		font-size: 0.7rem;
		color: var(--color-error);
		margin-top: 0.25rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.source-url {
		font-family: var(--font-mono);
		font-size: 0.7rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
