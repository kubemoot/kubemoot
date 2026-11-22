<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { refreshTrigger } from '#lib/stores/index.js';
	import { DetailPanel } from '#lib/components/layout/index.js';
	import { Section, InfoRow, PageTitle } from '#lib/components/common/index.js';
	import HealthBadge from '#lib/components/common/HealthBadge.svelte';
	import type { Health } from '#lib/health.js';
	import { errorOnFailure, loadingOnStart } from '#lib/client/refresh-state.js';
	import { formatTimestamp } from '#lib/discussion-timing.js';
	import { formatUptime, type OperatorDetail } from '#lib/operator-detail.js';

	interface ComponentStatus {
		name: string;
		healthy: boolean;
		message: string;
	}

	function reportHealth(r: ComponentStatus | null | undefined): Health {
		if (!r) return 'unknown';
		return r.healthy ? 'healthy' : 'down';
	}

	let detail = $state<OperatorDetail | null>(null);
	// The operator's own health report: undefined while loading, null when it could not be read.
	let report = $state<ComponentStatus | null | undefined>(undefined);
	let loaded = false;
	let loading = $state(true);
	let error = $state<string | null>(null);
	let now = $state(Date.now());

	async function fetchDetail() {
		loading = loadingOnStart(loaded);
		error = null;
		try {
			const res = await fetch(resolve('/api/kubemoot/operator'));
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			detail = await res.json();
			now = Date.now();
		} catch (e) {
			error = errorOnFailure(loaded, e, 'Failed to read the operator');
		} finally {
			loaded = loaded || error === null;
			loading = false;
		}
		await fetchReport();
	}

	async function fetchReport() {
		try {
			const res = await fetch(resolve('/api/kubemoot/componentstatuses'));
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const all = (await res.json()) as ComponentStatus[];
			report = all.find((c) => c.name === 'operator') ?? null;
		} catch {
			report = null;
		}
	}

	onMount(() => {
		fetchDetail();
	});

	$effect(() => {
		$refreshTrigger;
		fetchDetail();
	});

	const deployment = $derived(detail?.deployment.available ? detail.deployment.data : undefined);
	const resource = $derived(deployment?.resource ?? null);
	const health = $derived<Health>(reportHealth(report));
	const started = $derived(detail?.startedAt.available ? detail.startedAt.data : undefined);
	const uptime = $derived(started ? formatUptime(now - Date.parse(started)) : '');
</script>

<DetailPanel route="/operator" title="Operator" {loading} {error} {resource}>
	{#snippet header()}
		<div class="header-content">
			<div class="title-section">
				<span class="kind">Deployment</span>
				<PageTitle route="/operator" />
			</div>
			<HealthBadge {health} size="md" />
		</div>
	{/snippet}

	{#snippet children()}
		{#if detail}
			<div class="sections">
				<Section title="Summary">
					{#if detail.deployment.available}
						<InfoRow label="Version" value={detail.deployment.data.version} mono />
						<InfoRow label="Image" value={detail.deployment.data.image} mono />
						<InfoRow label="Ready replicas" value="{detail.deployment.data.ready} of {detail.deployment.data.desired}" />
					{:else}
						<InfoRow label="Version"><span class="refused">{detail.deployment.reason}</span></InfoRow>
					{/if}
					<InfoRow label="Uptime">
						{#if detail.startedAt.available}
							{#if started}{uptime}, since {formatTimestamp(started)}{:else}<span class="refused">no operator pod is running</span>{/if}
						{:else}
							<span class="refused">{detail.startedAt.reason}</span>
						{/if}
					</InfoRow>
					<InfoRow label="Leader">
						{#if detail.leader.available}
							{#if detail.leader.data}<span class="mono">{detail.leader.data}</span>{:else}<span class="refused">no operator pod holds the lease</span>{/if}
						{:else}
							<span class="refused">{detail.leader.reason}</span>
						{/if}
					</InfoRow>
					<InfoRow label="Settings">
						<a href={resolve('/config')}>Kubemoot Config</a>
					</InfoRow>
				</Section>

				<Section title="Health">
					<InfoRow label="Operator report">
						{#if report === undefined}
							<span class="refused">Reading</span>
						{:else if report === null}
							<span class="refused">The operator's health report could not be read</span>
						{:else}
							{report.healthy ? 'Ready' : 'Not ready'}{report.message ? `: ${report.message}` : ''}
						{/if}
					</InfoRow>
					{#if detail.deployment.available && detail.deployment.data.conditions.length > 0}
						<div class="table-wrap">
							<table data-testid="conditions">
								<thead>
									<tr><th scope="col">Condition</th><th scope="col">Status</th><th scope="col">Reason</th><th scope="col">Since</th><th scope="col">Message</th></tr>
								</thead>
								<tbody>
									{#each detail.deployment.data.conditions as c (c.type)}
										<tr class:problem={c.status !== 'True'}>
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
					{/if}
				</Section>

				<Section title={detail.crds.available ? `Registered resource types (${detail.crds.data.length})` : 'Registered resource types'}>
					{#if !detail.crds.available}
						<p class="refused" role="status">{detail.crds.reason}</p>
					{:else if detail.crds.data.length === 0}
						<p class="muted">No Kubemoot resource types are registered in this cluster.</p>
					{:else}
						<div class="table-wrap">
							<table data-testid="crds">
								<thead>
									<tr><th scope="col">Name</th><th scope="col">Kind</th><th scope="col">Scope</th><th scope="col">Versions</th></tr>
								</thead>
								<tbody>
									{#each detail.crds.data as crd (crd.name)}
										<tr>
											<th scope="row" class="mono">{crd.name}</th>
											<td>{crd.kind}</td>
											<td>{crd.scope}</td>
											<td class="mono">
												{#each crd.versions as v, i (v.name)}{i > 0 ? ', ' : ''}{v.name}{v.storage ? ' (stored)' : ''}{v.served ? '' : ' (not served)'}{/each}
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}
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
	.table-wrap {
		overflow-x: auto;
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
	.mono {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}
	.refused,
	.muted {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		margin: 0;
	}
	.refused {
		color: var(--color-warning);
	}
</style>
