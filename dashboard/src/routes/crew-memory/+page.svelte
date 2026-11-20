<script lang="ts">
	import { resolve } from '$app/paths';
	import { crewDirectory, namespace } from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry } from '#lib/crew-display-name.js';
	import { CommandHint } from '#lib/components/common/index.js';
	import { memoryExportCommand } from '#lib/cli-commands.js';
	import MemoryExportPanel from '#lib/components/memory/MemoryExportPanel.svelte';

	interface Fact {
		namespace: string;
		crew: string;
		topic: string;
		key: string;
		value: string;
		learnedBy: string;
		learnedAt: string;
		usedAt: string;
	}

	// Memory keys are <namespace>.<crew>.<topic>.<key>. The crew is the one picked in
	// the top bar, the same selection the Discussions page uses.
	const scope = $derived<CrewScope | null>(
		$namespace === '' ? null : tryCrewScope($namespace, crewEntry($crewDirectory, $namespace)?.crew)
	);

	let facts = $state<Fact[]>([]);
	let loading = $state(false);
	let error = $state<string | null>(null);

	async function load(current: CrewScope | null) {
		error = null;
		if (!current) {
			facts = [];
			return;
		}
		loading = true;
		try {
			const q = new URLSearchParams({ crew: current.crew, namespace: current.namespace });
			const res = await fetch(`${resolve('/api/kubemoot/crew-memory')}?${q}`);
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			facts = (data.facts || []).sort((a: Fact, b: Fact) => b.usedAt.localeCompare(a.usedAt));
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load crew memory';
		} finally {
			loading = false;
		}
	}

	// Reload whenever the picked crew changes.
	$effect(() => {
		load(scope);
	});

	// Export: the operator builds the OMPI export; the panel previews it and offers the download.
	let exportOpen = $state(false);
	const exportEndpoint = resolve('/api/kubemoot/crew-memory/export');
</script>

<div class="page">
	<div class="page-header">
		<h1>Crew Memory</h1>
		{#if scope}
			<div class="header-actions">
				<button onclick={() => (exportOpen = !exportOpen)} aria-expanded={exportOpen} title="Download this crew's memory in the OMPI interchange format">Export</button>
			</div>
		{/if}
	</div>
	<p class="sub">
		Working memory is what a crew has learned about this cluster and keeps between discussions, such as which accelerator
		holds which model. Facts are scoped to one crew, and the least recently used are dropped over time.
	</p>

	{#if error}<p class="error" role="alert">{error}</p>{/if}

	{#if scope && exportOpen}
		<MemoryExportPanel {scope} endpoint={exportEndpoint} />
	{/if}

	{#if !scope}
		<div class="empty-state">Pick one crew in the top bar to see what it remembers.</div>
	{:else}
		<div class="card">
			{#if loading && facts.length === 0}
				<div class="empty-state">Loading...</div>
			{:else if facts.length === 0}
				<div class="empty-state">This crew has not learned any facts yet. Facts appear here as its agents discover things.</div>
			{:else}
				<table>
					<thead>
						<tr><th>Topic</th><th>Key</th><th>Value</th><th>Learned by</th><th>Last used</th></tr>
					</thead>
					<tbody>
						{#each facts as f (f.namespace + '.' + f.topic + '.' + f.key)}
							<tr>
								<td>{f.topic}</td>
								<td>{f.key}</td>
								<td class="val">{f.value}</td>
								<td>{f.learnedBy}</td>
								<td class="ts">{f.usedAt}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>

		<p class="hint">This page only reads. Facts are written by the crew's agents; to take a copy, run:</p>
		<CommandHint label="Export this crew's memory:" command={memoryExportCommand(scope.crew, scope.namespace)} />
	{/if}
</div>

<style>
	.page { padding: 1.5rem; max-width: 1100px; }
	.header-actions { display: flex; gap: 0.5rem; }
	.page-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
	h1 { color: var(--color-text); }
	.sub { color: var(--color-text-muted); margin-top: 0.25rem; max-width: 70ch; }
	.card { background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.5rem; overflow-x: auto; }
	table { width: 100%; border-collapse: collapse; font-size: 0.9rem; color: var(--color-text); }
	th, td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid var(--color-border); vertical-align: top; }
	th { color: var(--color-text-muted); font-weight: 600; font-size: 0.8rem; }
	tbody tr:last-child td { border-bottom: none; }
	.val { max-width: 420px; word-break: break-word; }
	.ts { white-space: nowrap; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
	.empty-state { color: var(--color-text-muted); text-align: center; padding: 2rem 1rem; }
	.hint { color: var(--color-text-muted); font-size: 0.85rem; }
	button {
		padding: 0.3rem 0.7rem; cursor: pointer; font-size: 0.8rem;
		background: var(--color-bg-tertiary); color: var(--color-text);
		border: 1px solid var(--color-border); border-radius: 0.375rem;
	}
	.error { color: var(--color-error); }
</style>
