<script lang="ts">
	import { base } from '$app/paths';
	import type { MCPServerReport } from '$types/kubemoot.js';
	import ResourceCard from './ResourceCard.svelte';

	interface Props {
		report: MCPServerReport;
	}

	let { report }: Props = $props();

	const status = $derived(
		report.status?.verdict === 'use'
			? 'success'
			: report.status?.verdict === 'avoid'
				? 'error'
				: report.status?.verdict === 'caution'
					? 'warning'
					: 'pending'
	);

	const statusLabel = $derived(report.status?.verdict || 'untested');
	const trialCount = $derived(report.status?.trials?.length || 0);
</script>

<ResourceCard
	name={report.spec?.serverName || report.metadata.name}
	kind="MCPServerReport"
	href="{base}/mcpreports/{report.metadata.name}?namespace={report.metadata.namespace}"
	{status}
	{statusLabel}
>
	{#snippet children()}
		<div class="info">
			<div class="row">
				<span class="label">Success Rate</span>
				<span class="value">{report.status?.successRate || 'N/A'}</span>
			</div>
			<div class="row">
				<span class="label">Transport</span>
				<span class="value">{report.status?.recommendedTransport || '-'}</span>
			</div>
			<div class="row">
				<span class="label">Registry</span>
				<span class="value">{report.spec?.registryType || '-'}</span>
			</div>
		</div>
	{/snippet}

	{#snippet footer()}
		<div class="footer-info">
			<span class="trial-count">{trialCount} trial{trialCount !== 1 ? 's' : ''}</span>
			{#if report.status?.lastTested}
				<span class="last-tested">Tested {new Date(report.status.lastTested).toLocaleDateString()}</span>
			{/if}
		</div>
	{/snippet}
</ResourceCard>

<style>
	.info {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.value {
		font-size: 0.8rem;
		font-weight: 500;
	}

	.footer-info {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
