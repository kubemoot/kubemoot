<script lang="ts">
	import { readinessStatus } from '#lib/resource-status.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { namespace, refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, StatusBadge } from '#lib/components/common/index.js';
	import type { RAGSource } from '#lib/types/kubemoot.js';

	let source = $state<RAGSource | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);
	const ns = $derived(page.url.searchParams.get('namespace') || $namespace);

	async function fetchSource() {
		loading = true;
		error = null;

		try {
			const res = await fetch(`${resolve('/api/kubemoot/ragsources/[name]', { name })}?namespace=${ns}`);
			if (!res.ok) throw new Error('RAG source not found');
			source = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch RAG source';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchSource();
	});

	$effect(() => {
		ns;
		$refreshTrigger;
		fetchSource();
	});

	const status = $derived(readinessStatus(source?.status));
</script>

<DetailPanel title={name} subtitle="RAGSource" {loading} {error}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">RAGSource</span>
				<h1 class="title">{name}</h1>
			</div>
			{#if source}
				<StatusBadge {status} label={source.status?.phase || 'Unknown'} />
			{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if source}
			<div class="sections">
				<Section title="Source">
					<InfoRow label="Type" value={source.spec.source.type} />
					{#if source.spec.source.gitUrl}
						<InfoRow label="Git URL" value={source.spec.source.gitUrl} mono />
						<InfoRow label="Branch" value={source.spec.source.branch} />
					{/if}
					{#if source.spec.source.webUrl}
						<InfoRow label="Web URL" value={source.spec.source.webUrl} mono />
					{/if}
					{#if source.spec.source.s3Bucket}
						<InfoRow label="S3 Bucket" value={source.spec.source.s3Bucket} />
						<InfoRow label="S3 Prefix" value={source.spec.source.s3Prefix} />
					{/if}
				</Section>

				<Section title="Vector Store">
					<InfoRow label="Type" value={source.spec.vectorStore.type} />
					<InfoRow label="Endpoint" value={source.spec.vectorStore.endpoint} mono />
					<InfoRow label="Collection" value={source.spec.vectorStore.collectionName} />
					<InfoRow label="Embedding Model" value={source.spec.embeddingModelRef} />
				</Section>

				{#if source.spec.chunking}
					<Section title="Chunking">
						<InfoRow label="Chunk Size" value={source.spec.chunking.chunkSize} />
						<InfoRow label="Chunk Overlap" value={source.spec.chunking.chunkOverlap} />
					</Section>
				{/if}

				<Section title="Status">
					<InfoRow label="Ready" value={source.status?.ready ? 'Yes' : 'No'} />
					<InfoRow label="Phase" value={source.status?.phase} />
					<InfoRow label="Query Endpoint" value={source.status?.queryEndpoint} mono />
					<InfoRow label="Message" value={source.status?.message} />
				</Section>

				{#if source.status?.indexStatus}
					<Section title="Index Status">
						<InfoRow label="Documents" value={source.status.indexStatus.documentCount} />
						<InfoRow label="Chunks" value={source.status.indexStatus.chunkCount} />
						<InfoRow label="Last Indexed" value={source.status.indexStatus.lastIndexed} />
						<InfoRow label="Index Duration" value={source.status.indexStatus.indexDuration} />
					</Section>
				{/if}

				<Section title="Metadata" collapsible defaultOpen={false}>
					<InfoRow label="Namespace" value={source.metadata.namespace} />
					<InfoRow label="UID" value={source.metadata.uid} mono />
					<InfoRow label="Created" value={source.metadata.creationTimestamp} />
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
</style>
