<script lang="ts">
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, PageTitle } from '#lib/components/common/index.js';
	import HealthBadge from '#lib/components/common/HealthBadge.svelte';
	import { formatTimestamp } from '#lib/discussion-timing.js';
	import { acceleratorFacts, capacityRows, conditionRows, podPhaseOk, staticFacts, taintText } from '#lib/node-detail.js';
	import { nodeHealth, nodeProblems, nodeUse } from '#lib/node-usage.js';
	import type { NodeWithAccelerator } from '#lib/types/k8s.js';
	import type { NodeMetric } from '#lib/node-usage.js';
	import type { NodePod, Reading } from '#lib/reading.js';

	let node = $state<NodeWithAccelerator | null>(null);
	let metrics = $state<Reading<Record<string, NodeMetric>> | null>(null);
	let pods = $state<Reading<NodePod[]> | null>(null);
	let loaded = false;
	let loading = $state(true);
	let error = $state<string | null>(null);

	const name = $derived(page.params.name as string);

	async function fetchNode() {
		loading = loadingOnStart(loaded);
		error = null;
		try {
			const res = await fetch(resolve('/api/nodes/[name]', { name }));
			const body = await res.json();
			if (!res.ok) throw new Error(body.error ?? 'Node not found');
			node = body.node;
			metrics = body.metrics;
			pods = body.pods;
		} catch (e) {
			error = errorOnFailure(loaded, e, 'Failed to fetch node');
		} finally {
			loaded = loaded || error === null;
			loading = false;
		}
	}

	onMount(() => {
		fetchNode();
	});

	$effect(() => {
		name;
		$refreshTrigger;
		fetchNode();
	});

	const metric = $derived(metrics?.available && node ? metrics.data[node.metadata.name] : undefined);
	const podList = $derived(pods?.available ? pods.data : undefined);
	const running = $derived(podList?.filter((p) => p.phase === 'Running').length);
	const use = $derived(node ? nodeUse(node, metric) : {});
	const health = $derived(node ? nodeHealth(node, use) : 'unknown');
	const problems = $derived(node ? nodeProblems(node, use) : []);
	const conditions = $derived(node ? conditionRows(node) : []);
	const capacity = $derived(node ? capacityRows(node, metric, running) : []);
	const requested = $derived(podList?.reduce((sum, p) => sum + p.accelerators, 0));
	const accelerator = $derived(node ? acceleratorFacts(node, requested) : undefined);
	const facts = $derived(node ? staticFacts(node) : []);
	const taints = $derived(node?.spec?.taints ?? []);
	const labels = $derived(Object.entries(node?.metadata.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)));
</script>

<DetailPanel route="/nodes/[name]" title={name} {loading} {error} resource={node}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">Node</span>
				<PageTitle route="/nodes/[name]" title={name} />
			</div>
			{#if node}<HealthBadge health={health} size="md" />{/if}
		</div>
	{/snippet}

	{#snippet children()}
		{#if node}
			<div class="sections">
				{#if problems.length > 0}
					<ul class="problems" data-testid="node-problems">
						{#each problems as p (p.text)}<li>{p.text}</li>{/each}
					</ul>
				{/if}

				<Section title="Conditions">
					<div class="table-wrap">
						<table data-testid="conditions">
							<thead>
								<tr><th scope="col">Condition</th><th scope="col">Status</th><th scope="col">Reason</th><th scope="col">Since</th><th scope="col">Message</th></tr>
							</thead>
							<tbody>
								{#each conditions as c (c.type)}
									<tr class:problem={c.problem}>
										<th scope="row">{c.type}</th>
										<td>{c.status}</td>
										<td>{c.reason ?? ''}</td>
										<td>{formatTimestamp(c.lastTransitionTime)}</td>
										<td class="message">{c.message ?? ''}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</Section>

				<Section title="Capacity and use">
					{#if metrics && !metrics.available}
						<p class="muted" role="status">CPU and memory use are not shown: {metrics.reason}</p>
					{/if}
					{#if pods && !pods.available}
						<p class="muted" role="status">Pods are not shown: {pods.reason}</p>
					{/if}
					<div class="table-wrap">
						<table data-testid="capacity">
							<thead>
								<tr><th scope="col">Resource</th><th scope="col">Capacity</th><th scope="col">Allocatable</th><th scope="col">In use</th></tr>
							</thead>
							<tbody>
								{#each capacity as r (r.resource)}
									<tr><th scope="row">{r.resource}</th><td>{r.capacity}</td><td>{r.allocatable}</td><td>{r.inUse}</td></tr>
								{/each}
							</tbody>
						</table>
					</div>
				</Section>

				{#if accelerator}
					<Section title="Accelerators">
						<InfoRow label="Type" value={accelerator.type} />
						<InfoRow label="Devices" value={accelerator.count} />
						<InfoRow label="Allocatable" value={accelerator.allocatable} />
						<InfoRow label="Memory each" value={accelerator.memory} />
						{#if accelerator.requested !== undefined}
							<InfoRow label="Asked for by pods" value="{Math.min(accelerator.requested, accelerator.count)} of {accelerator.count}" />
						{/if}
					</Section>
				{/if}

				<Section title={podList ? `Pods (${podList.length})` : 'Pods'}>
					{#if podList && podList.length === 0}
						<p class="muted">No pods run on this node.</p>
					{:else if podList}
						<div class="table-wrap pods">
							<table data-testid="pods">
								<thead>
									<tr><th scope="col">Pod</th><th scope="col">Namespace</th><th scope="col">Phase</th><th scope="col" class="num">Restarts</th></tr>
								</thead>
								<tbody>
									{#each podList as p (p.namespace + '/' + p.name)}
										<tr class:problem={!podPhaseOk(p.phase)}>
											<th scope="row" class="mono">{p.name}</th>
											<td>{p.namespace}</td>
											<td>{p.phase}</td>
											<td class="num" class:restarts={p.restarts > 0}>{p.restarts}</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}
				</Section>

				<Section title="Taints" collapsible defaultOpen={taints.length > 0}>
					{#if taints.length === 0}
						<p class="muted">None.</p>
					{:else}
						<ul class="chips">{#each taints as t (taintText(t))}<li class="mono">{taintText(t)}</li>{/each}</ul>
					{/if}
				</Section>

				<Section title="Labels" collapsible defaultOpen={false}>
					<ul class="chips">{#each labels as [k, v] (k)}<li class="mono">{k}{v ? `=${v}` : ''}</li>{/each}</ul>
				</Section>

				<Section title="Details">
					{#each facts as f (f.label)}
						<InfoRow label={f.label} value={f.value} mono />
					{/each}
				</Section>
			</div>
		{/if}
	{/snippet}
</DetailPanel>

<style>
	.header-content {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}
	.kind {
		font-size: 0.7rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}
	.sections {
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
	}
	.problems {
		list-style: none;
		margin: 0;
		padding: 0.6rem 0.9rem;
		border-radius: 0.375rem;
		background: rgba(239, 68, 68, 0.1);
		border: 1px solid rgba(239, 68, 68, 0.3);
		font-size: 0.85rem;
	}
	.table-wrap {
		overflow-x: auto;
	}
	.table-wrap.pods {
		max-height: 22rem;
		overflow-y: auto;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.8rem;
	}
	th,
	td {
		text-align: left;
		padding: 0.4rem 0.6rem;
		border-bottom: 1px solid var(--color-border);
		vertical-align: top;
	}
	thead th {
		color: var(--color-text-muted);
		font-weight: 500;
		position: sticky;
		top: 0;
		background: var(--color-bg-secondary);
	}
	tbody th {
		font-weight: 500;
	}
	tr.problem td,
	tr.problem th {
		color: var(--color-error);
	}
	.message {
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
	}
	.num {
		text-align: right;
	}
	.restarts {
		color: var(--color-warning);
		font-weight: 600;
	}
	.mono {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}
	.muted {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		margin: 0 0 0.5rem;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.chips li {
		background: var(--color-bg-tertiary);
		border-radius: 0.25rem;
		padding: 0.15rem 0.45rem;
		overflow-wrap: anywhere;
	}
</style>
