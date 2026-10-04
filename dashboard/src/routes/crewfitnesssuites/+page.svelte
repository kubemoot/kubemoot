<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { crewDirectory, namespace } from '#lib/stores/index.js';
	import { crewLabel, crewTooltip } from '#lib/crew-display-name.js';
	import { LiveList } from '#lib/client/liveList.svelte.js';
	import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';
	import { suiteDisplayPhase, suiteIsJudged } from '#lib/fitness-suite-controls.js';

	// Push-based live list: initial fetch + SSE watch on CrewFitnessSuite. The
	// operator advances each suite's status (phase, iterationsCompleted,
	// passed/failed/errored) on the CR as children finish and on the terminal
	// transition, so MODIFIED events keep the status badge, progress, and counts
	// current in place - no manual refresh, and the badge flips Running→Completed
	// the moment the operator does.
	const live = new LiveList<CrewFitnessSuite>('crewfitnesssuites');
	// Ticking clock so a running suite's elapsed Duration and ETA advance every
	// second, not only when an SSE status change happens to arrive.
	let now = $state(Date.now());
	onMount(() => {
		live.start($namespace);
		const tick = setInterval(() => { now = Date.now(); }, 1000);
		refreshJudge();
		const judgeTick = setInterval(() => void refreshJudge(), 10_000);
		return () => { clearInterval(tick); clearInterval(judgeTick); live.stop(); };
	});
	$effect(() => {
		live.setNamespace($namespace);
	});

	function phaseClass(phase?: string): string {
		switch (phase) {
			case 'Completed': return 'phase-pass';
			case 'Failed': case 'Error': return 'phase-fail';
			case 'Running': return 'phase-running';
			case 'Paused': case 'Pausing': case 'Stopping': return 'phase-paused';
			case 'Cancelled': return 'phase-cancelled';
			default: return 'phase-pending';
		}
	}

	function formatDuration(startedAt?: string, completedAt?: string): string {
		if (!startedAt) return '-';
		const start = new Date(startedAt).getTime();
		const end = completedAt ? new Date(completedAt).getTime() : now;
		const ms = end - start;
		if (ms < 1000) return `${ms}ms`;
		if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
		const mins = Math.floor(ms / 60_000);
		const secs = Math.floor((ms % 60_000) / 1000);
		return `${mins}m ${secs}s`;
	}

	// ETA for a running suite: remaining iterations × mean wall-clock per completed
	// iteration so far. Recomputes each tick and each iterationsCompleted increment.
	function etaText(s?: CrewFitnessSuite['status']): string {
		if (s?.phase !== 'Running' || !s?.startedAt) return '';
		const total = s.iterationsTotal ?? 0;
		const done = s.iterationsCompleted ?? 0;
		if (done <= 0 || total <= done) return '';
		const elapsed = now - new Date(s.startedAt).getTime();
		const remainingMs = (total - done) * (elapsed / done);
		const mins = Math.floor(remainingMs / 60_000);
		const secs = Math.round((remainingMs % 60_000) / 1000);
		return mins >= 1 ? `~${mins}m ${secs}s left` : `~${secs}s left`;
	}

	function formatBytes(n?: number): string {
		if (!n) return '-';
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / 1024 / 1024).toFixed(2)} MB`;
	}

	// Post-suite DEFER/REFLECTS judging progress, per suite. The judge pass runs
	// AFTER a suite reaches a terminal phase and scores one scenario at a time
	// (resumable v2 checkpoint), so a terminal suite can still be "judging X/Y" for
	// a while. Poll /scores for terminal-but-not-complete suites until complete.
	let judge = $state<Record<string, { judged: number; complete: boolean }>>({});
	async function refreshJudge() {
		for (const suite of live.items) {
			if (!suiteIsJudged(suite.status?.phase)) continue; // Cancelled suites are never judged
			const ns = suite.metadata.namespace as string;
			const name = suite.metadata.name;
			const key = `${ns}/${name}`;
			if (judge[key]?.complete) continue; // done - stop polling this one
			try {
				const r = await fetch(resolve('/api/kubemoot/crewfitnesssuites/[namespace]/[name]/scores', { namespace: ns, name }));
				if (!r.ok) continue;
				const d = await r.json();
				judge = { ...judge, [key]: { judged: d.judged ?? 0, complete: !!d.complete } };
			} catch {
				/* transient; retry next tick */
			}
		}
	}
</script>

<div class="page-header">
	<h1>Fitness Suites</h1>
	<span class="count">{live.items.length}</span>
</div>

<p class="page-subtitle">
	N-iteration sweeps of fitness scripts against a crew. Each suite produces an XLSX
	with per-iteration results and per-scenario aggregates.
</p>

{#if live.loading}
	<p class="status-msg">Loading fitness suites...</p>
{:else if live.error}
	<p class="status-msg error">{live.error}</p>
{:else if live.items.length === 0}
	<p class="status-msg">
		No CrewFitnessSuite CRs found in namespace <code>{$namespace || 'all'}</code>.
		Apply one with <code>kubectl apply -f &lt;suite.yaml&gt;</code> to start a baseline sweep.
	</p>
{:else}
	<table class="suites-table">
		<thead>
			<tr>
				<th>Status</th>
				<th>Name</th>
				<th>Crew</th>
				<th>Progress</th>
				<th>Pass / Fail / Err</th>
				<th>Duration</th>
				<th>Artifact</th>
				<th>Age</th>
			</tr>
		</thead>
		<tbody>
			{#each live.items as suite (suite.metadata.namespace + '/' + suite.metadata.name)}
				{@const s = suite.status}
				{@const total = s?.iterationsTotal ?? 0}
				{@const done = s?.iterationsCompleted ?? 0}
				{@const ns = suite.metadata.namespace as string}
				{@const name = suite.metadata.name}
				{@const jp = judge[ns + '/' + name]}
				{@const scen = suite.spec.scripts?.length ?? 0}
				{@const shown = suiteDisplayPhase(suite)}
				<tr>
					<td>
						<span class="phase-badge {phaseClass(shown)}">
							{shown ?? 'Unknown'}
						</span>
					</td>
					<td class="mono">
						{name}
						{#if suite.spec.description}
							<div class="suite-desc">{suite.spec.description}</div>
						{/if}
					</td>
					<td>
						<a href="{resolve('/crews/[name]', { name: suite.spec.crewRef })}?namespace={ns}" class="crew-link" title={crewTooltip($crewDirectory, ns, suite.spec.crewRef)}>
							{crewLabel($crewDirectory, ns, suite.spec.crewRef)}
						</a>
					</td>
					<td class="mono">
						{done} / {total}
						{#if etaText(s)}<div class="muted small">{etaText(s)}</div>{/if}
					</td>
					<td>
						<span class="passed">{s?.passed ?? 0}</span> /
						<span class="failed">{s?.failed ?? 0}</span> /
						<span class="errored">{s?.errored ?? 0}</span>
					</td>
					<td class="mono">{formatDuration(s?.startedAt, s?.completedAt)}</td>
					<td>
						{#if s?.artifactRef?.objectKey}
							<a
								href="{resolve('/api/kubemoot/crewfitnesssuites/[namespace]/[name]/artifact', { namespace: ns, name })}"
								class="download-link"
								download
								title="Download {formatBytes(s.artifactRef.sizeBytes)}"
							>
								⬇ XLSX
							</a>
							<span class="muted small">({formatBytes(s.artifactRef.sizeBytes)})</span>
						{:else if s?.phase === 'Running' || s?.phase === 'Pending' || s?.phase === 'Paused'}
							<span class="muted small">pending</span>
						{:else}
							<span class="muted small">-</span>
						{/if}
						{#if jp && !jp.complete && scen > 0}
							<div class="muted small">⏳ judging {jp.judged}/{scen}</div>
						{:else if jp?.complete}
							<div class="muted small">judged ✓</div>
						{/if}
					</td>
					<td class="muted">{suite.metadata.creationTimestamp ? new Date(suite.metadata.creationTimestamp).toLocaleString() : '-'}</td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}

<style>
	.page-header {
		display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.5rem;
	}
	.page-header h1 { font-size: 1.5rem; font-weight: 600; }
	.count {
		background: var(--color-bg-tertiary); padding: 0.15rem 0.6rem;
		border-radius: 999px; font-size: 0.8rem; color: var(--color-text-muted);
	}
	.page-subtitle {
		color: var(--color-text-muted); margin-bottom: 1.5rem; font-size: 0.9rem;
	}

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }
	.status-msg code { color: var(--color-cyan); font-family: var(--font-mono); }

	.suites-table {
		width: 100%; border-collapse: collapse;
	}
	.suites-table th, .suites-table td {
		text-align: left; padding: 0.5rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
	}
	.suites-table th { font-weight: 600; color: var(--color-text-muted); font-size: 0.85rem; }
	.mono { font-family: var(--font-mono); font-size: 0.9rem; }
	.muted { color: var(--color-text-muted); }
	.small { font-size: 0.8rem; }
	.suite-desc {
		font-family: var(--font-sans); font-size: 0.78rem;
		color: var(--color-text-muted); margin-top: 0.2rem;
		max-width: 32rem; white-space: normal; line-height: 1.3;
	}

	.phase-badge {
		display: inline-block; padding: 0.15rem 0.55rem; border-radius: 4px;
		font-size: 0.75rem; font-weight: 600; text-transform: uppercase;
	}
	.phase-pass { background: var(--color-success-bg); color: var(--color-success); }
	.phase-fail { background: var(--color-error-bg); color: var(--color-error); }
	.phase-running { background: var(--color-cyan-bg); color: var(--color-cyan); }
	.phase-pending { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.phase-paused { background: var(--color-warning-bg, rgba(204, 153, 51, 0.15)); color: var(--color-warning, #c93); }
	.phase-cancelled { background: var(--color-bg-tertiary); color: var(--color-text-muted); text-decoration: line-through; }

	.passed { color: var(--color-success); font-weight: 600; }
	.failed { color: var(--color-error); font-weight: 600; }
	.errored { color: var(--color-warning, #c66); font-weight: 600; }

	.crew-link, .download-link {
		color: var(--color-cyan); text-decoration: none;
	}
	.crew-link:hover, .download-link:hover { text-decoration: underline; }
	.download-link {
		font-family: var(--font-mono); font-size: 0.85rem;
		padding: 0.15rem 0.4rem; border: 1px solid var(--color-cyan);
		border-radius: 4px;
	}
</style>
