<script lang="ts">
	// Kubemoot Dashboard - Overview Page
	import { onMount } from 'svelte';
	import { base } from '$app/paths';
	import { namespace, refreshTrigger } from '$stores';
	import { StatusBadge } from '$components/common';
	import { nodeCounts, readyCount, type CountEntry } from '$lib/overview-counts';
	import { readyCountStatus } from '$lib/resource-status';

	type ResourceCounts = Record<string, CountEntry>;

	let counts = $state<ResourceCounts>({
		nodes: { total: 0, ready: 0 },
		gpus: { total: 0, ready: 0 },
		modelproviders: { total: 0, ready: 0 },
		models: { total: 0, ready: 0 },
		embeddingmodels: { total: 0, ready: 0 },
		mcpservers: { total: 0, ready: 0 },
		mcpgateways: { total: 0, ready: 0 },
		ragsources: { total: 0, ready: 0 },
		agents: { total: 0, ready: 0 }
	});

	let error = $state<string | null>(null);

	// Control-plane component health, served by the operator's /componentstatuses
	// (operator self, NATS, model providers). Cluster-scoped, so it isn't filtered
	// by the namespace selector. Degrades gracefully when the endpoint is absent.
	interface ComponentStatus {
		name: string;
		healthy: boolean;
		message: string;
	}
	let controlPlane = $state<ComponentStatus[] | null>(null);
	let cpUnavailable = $state(false);

	async function fetchComponentStatuses() {
		try {
			const r = await fetch(`${base}/api/kubemoot/componentstatuses`);
			if (!r.ok) throw new Error(`HTTP ${r.status}`);
			controlPlane = await r.json();
			cpUnavailable = false;
		} catch {
			controlPlane = null;
			cpUnavailable = true;
		}
	}

	// The CRD lists counted on the Overview, in card order after nodes and GPUs.
	const CRD_PLURALS = [
		'modelproviders',
		'models',
		'embeddingmodels',
		'mcpservers',
		'mcpgateways',
		'ragsources',
		'agents'
	] as const;

	const getJson = (path: string) => fetch(`${base}${path}`).then((r) => r.json());

	async function fetchCounts() {
		error = null;

		try {
			const [nodes, ...lists] = await Promise.all([
				getJson('/api/nodes'),
				...CRD_PLURALS.map((plural) => getJson(`/api/kubemoot/${plural}?namespace=${$namespace}`))
			]);
			counts = {
				...nodeCounts(nodes.nodes || []),
				...Object.fromEntries(CRD_PLURALS.map((plural, i) => [plural, readyCount(lists[i].items)]))
			};
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to fetch resources';
		}
	}

	onMount(() => {
		fetchCounts();
		fetchComponentStatuses();
	});

	$effect(() => {
		$namespace;
		$refreshTrigger;
		fetchCounts();
	});

	$effect(() => {
		$refreshTrigger; // control plane is cluster-scoped — refresh on trigger, not namespace
		fetchComponentStatuses();
	});

	const resourceTypes = [
		{ key: 'nodes', label: 'Nodes', href: `${base}/nodes`, icon: 'server' },
		{ key: 'gpus', label: 'GPUs', href: `${base}/nodes`, icon: 'zap' },
		{ key: 'modelproviders', label: 'Model Providers', href: `${base}/modelproviders`, icon: 'cloud' },
		{ key: 'models', label: 'Models', href: `${base}/models`, icon: 'box' },
		{ key: 'embeddingmodels', label: 'Embedding Models', href: `${base}/embeddingmodels`, icon: 'layers' },
		{ key: 'mcpservers', label: 'MCP Servers', href: `${base}/mcpservers`, icon: 'terminal' },
		{ key: 'mcpgateways', label: 'MCP Gateways', href: `${base}/mcpgateways`, icon: 'merge' },
		{ key: 'ragsources', label: 'RAG Sources', href: `${base}/ragsources`, icon: 'database' },
		{ key: 'agents', label: 'Agents', href: `${base}/agents`, icon: 'cpu' }
	] as const;
</script>

<div class="overview">
	<header class="header">
		<h1>Overview</h1>
		{#if $namespace === ''}
			<p class="subtitle">Kubemoot resources across <em>all namespaces</em></p>
		{:else}
			<p class="subtitle">Kubemoot resources in namespace <code>{$namespace}</code></p>
		{/if}
	</header>

	{#if error}
		<div class="error-banner">
			<span>{error}</span>
		</div>
	{/if}

	<section class="control-plane">
		<h2 class="cp-title">Control plane</h2>
		{#if controlPlane && controlPlane.length}
			<div class="cp-row">
				{#each controlPlane as comp}
					<span class="cp-item" class:bad={!comp.healthy} title={comp.message}>
						<span class="cp-mark">{comp.healthy ? '✓' : '✗'}</span>
						<span class="cp-name">{comp.name}</span>
						<span class="cp-msg">{comp.message}</span>
					</span>
				{/each}
			</div>
		{:else if cpUnavailable}
			<p class="cp-note">Component status unavailable.</p>
		{:else}
			<p class="cp-note">Loading…</p>
		{/if}
	</section>

	<div class="grid">
		{#each resourceTypes as resource}
			{@const c = counts[resource.key]}
			<a href={resource.href} class="card">
				<div class="card-header">
					<span class="card-label">{resource.label}</span>
					<StatusBadge
						status={readyCountStatus(c.ready, c.total)}
						label={`${c.ready}/${c.total}`}
						size="sm"
					/>
				</div>
				<div class="card-count">{c.total}</div>
				<div class="card-footer">
					{c.ready} ready
				</div>
			</a>
		{/each}
	</div>
</div>

<style>
	.control-plane {
		margin-bottom: 1.25rem;
	}
	.cp-title {
		font-size: 0.78rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		margin: 0 0 0.5rem 0;
	}
	.cp-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 0.75rem;
	}
	.cp-item {
		display: inline-flex;
		align-items: baseline;
		gap: 0.4rem;
		padding: 0.3rem 0.6rem;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-secondary);
		font-size: 0.82rem;
	}
	.cp-mark {
		color: var(--color-success);
		font-weight: 700;
	}
	.cp-item.bad .cp-mark {
		color: var(--color-error);
	}
	.cp-name {
		font-weight: 600;
		color: var(--color-text);
	}
	.cp-msg {
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}
	.cp-note {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		font-style: italic;
		margin: 0;
	}

	.overview {
		max-width: 1200px;
	}

	.header {
		margin-bottom: 2rem;
	}

	.header h1 {
		font-size: 1.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.subtitle {
		margin-top: 0.5rem;
		color: var(--color-text-muted);
		font-size: 0.9rem;
	}

	.subtitle code {
		background-color: var(--color-bg-tertiary);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
		font-size: 0.85rem;
	}

	.error-banner {
		background-color: rgba(239, 68, 68, 0.15);
		border: 1px solid var(--color-error);
		color: var(--color-error);
		padding: 0.75rem 1rem;
		border-radius: 0.5rem;
		margin-bottom: 1.5rem;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
		gap: 1rem;
	}

	.card {
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1.25rem;
		transition: all 0.15s;
		text-decoration: none;
		color: inherit;
	}

	.card:hover {
		border-color: var(--color-primary);
		background-color: rgba(30, 41, 59, 0.8);
		text-decoration: none;
	}

	.card-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 0.75rem;
	}

	.card-label {
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--color-text-muted);
	}

	.card-count {
		font-size: 2.5rem;
		font-weight: 700;
		color: var(--color-text);
		line-height: 1;
	}

	.card-footer {
		margin-top: 0.5rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
