<script lang="ts">
	import { chartVersion } from '#lib/crew-chart.js';
	import { crewDisplayName, crewTechnicalHint } from '#lib/crew-display-name.js';
	import { WAITING_FOR_CAPACITY, capacityCondition, crewCanAnswer, crewPhaseLabel } from '#lib/crew-phase.js';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { namespace } from '#lib/stores/index.js';
	import { CommandHint, PageTitle } from '#lib/components/common/index.js';
	import { deleteCrewCommand } from '#lib/cli-commands.js';
	import type { Crew } from '#lib/types/kubemoot.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';

	const live = new LiveList<Crew>('crews');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});
</script>

<div class="page-header">
	<PageTitle route="/crews" />
	<span class="count">{live.items.length}</span>
</div>

{#if live.loading}
	<p class="status-msg">Loading crews...</p>
{:else if live.error}
	<p class="status-msg error">{live.error}</p>
{:else if live.items.length === 0}
	<p class="status-msg">No crews found in namespace <code>{$namespace}</code></p>
{:else}
	<div class="hint"><CommandHint label="Remove a crew:" command={deleteCrewCommand('NAME', 'NAMESPACE')} /></div>
	<div class="crew-grid">
		{#each live.items as crew (crew.metadata.namespace + '/' + crew.metadata.name)}
			{@const status = crew.status}
			{@const ready = crewCanAnswer(status?.phase, status?.ready)}
			{@const phase = status?.phase ?? 'Unknown'}
			{@const discussing = !!status?.discussionEndpoint}
			{@const version = chartVersion(crew.metadata.labels)}
			{@const technical = crewTechnicalHint(crew)}
			<div class="crew-item">
			<a href="{resolve('/crews/[name]', { name: crew.metadata.name })}?namespace={crew.metadata.namespace}" class="crew-card" class:ready class:discussing>
				<div class="card-top">
					<div class="crew-names">
						<div class="crew-name" title={technical}>{crewDisplayName(crew)}</div>
						{#if technical}
							<div class="crew-technical-name">{technical}</div>
						{/if}
					</div>
					<div class="badges">
						{#if version}
							<span class="chart-badge" title="Helm chart version">v{version}</span>
						{/if}
						<span class="phase-badge" class:phase-ready={phase === 'Ready'} class:phase-error={phase === 'Error'} class:phase-deploying={phase === 'Deploying'} class:phase-pending={phase === 'Pending'} class:phase-waiting={phase === WAITING_FOR_CAPACITY} title={capacityCondition(status?.conditions)?.message}>
							{crewPhaseLabel(phase)}
						</span>
					</div>
				</div>

				{#if crew.spec.description}
					<p class="crew-desc">{crew.spec.description}</p>
				{/if}

				<div class="crew-stats">
					{#if status?.agentCount != null}
						<div class="stat">
							<span class="stat-value">{status.agentCount}</span>
							<span class="stat-label">Agents</span>
						</div>
					{/if}
					{#if status?.coordinatorRef}
						<div class="stat">
							<span class="stat-value coord">{status.coordinatorRef}</span>
							<span class="stat-label">Coordinator</span>
						</div>
					{/if}
				</div>

				<div class="crew-footer">
					<span class="ns-label">{crew.metadata.namespace}</span>
					{#if discussing}
						<span class="discuss-badge" title="Discussion infrastructure is provisioned (coordinator endpoint reachable). Does not indicate whether a thread is currently in flight.">Discussion Enabled</span>
					{:else}
						<span class="discuss-off">Discussion Disabled</span>
					{/if}
				</div>
			</a>
			</div>
		{/each}
	</div>
{/if}

<style>
	.page-header {
		display: flex; align-items: center; gap: 0.75rem; margin-bottom: 1.5rem;
	}
	.count {
		background: var(--color-bg-tertiary); padding: 0.15rem 0.6rem;
		border-radius: 999px; font-size: 0.8rem; color: var(--color-text-muted);
	}

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }
	.status-msg code { color: var(--color-cyan); font-family: var(--font-mono); }

	.crew-grid {
		display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
		gap: 1rem;
	}

	.crew-item { display: flex; flex-direction: column; gap: 0.4rem; }
	.crew-item .crew-card { flex: 1; }
	.hint { margin-bottom: 1rem; }
	.crew-card {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem; padding: 1.25rem;
		text-decoration: none; color: var(--color-text);
		display: flex; flex-direction: column; gap: 0.75rem;
		transition: all 0.15s;
	}
	.crew-card:hover {
		border-color: var(--color-primary);
		text-decoration: none;
		transform: translateY(-1px);
	}
	.crew-card.discussing {
		border-left: 3px solid var(--color-success);
	}

	.card-top { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.25rem 0.5rem; }
	.crew-names { min-width: 0; }
	.crew-name { font-family: var(--font-mono); font-size: 1.1rem; font-weight: 600; color: var(--color-primary); overflow-wrap: break-word; }
	.crew-technical-name { font-family: var(--font-mono); font-size: 0.75rem; color: var(--color-text-muted); }
	.badges { display: flex; align-items: center; gap: 0.4rem; flex-shrink: 0; }

	.chart-badge {
		white-space: nowrap;
		font-size: 0.7rem; font-weight: 500; padding: 0.15rem 0.5rem;
		border-radius: 999px; font-family: var(--font-mono);
		background: var(--color-bg-tertiary); color: var(--color-text-muted);
		border: 1px solid var(--color-border);
	}

	.phase-badge {
		font-size: 0.7rem; font-weight: 600; padding: 0.15rem 0.5rem;
		border-radius: 999px; text-transform: uppercase; letter-spacing: 0.04em;
	}
	.phase-ready { background: rgba(34, 197, 94, 0.15); color: var(--color-success); }
	.phase-error { background: rgba(239, 68, 68, 0.15); color: var(--color-error); }
	.phase-deploying { background: rgba(59, 130, 246, 0.15); color: var(--color-primary); }
	.phase-pending { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.phase-waiting { background: rgba(234, 179, 8, 0.15); color: var(--color-warning, #eab308); }

	.crew-desc { font-size: 0.85rem; color: var(--color-text-muted); line-height: 1.4; }

	.crew-stats { display: flex; gap: 1.5rem; }
	.stat { display: flex; flex-direction: column; gap: 0.1rem; }
	.stat-value { font-size: 1.1rem; font-weight: 600; }
	.stat-value.coord { font-family: var(--font-mono); font-size: 0.8rem; color: var(--color-cyan); }
	.stat-label { font-size: 0.65rem; color: var(--color-text-muted); text-transform: uppercase; letter-spacing: 0.05em; }

	.crew-footer {
		display: flex; justify-content: space-between; align-items: center;
		padding-top: 0.5rem; border-top: 1px solid var(--color-border);
		font-size: 0.75rem;
	}
	.ns-label { color: var(--color-text-muted); font-family: var(--font-mono); }
	.discuss-badge { color: var(--color-success); font-weight: 500; }
	.discuss-off { color: var(--color-text-muted); }
</style>
