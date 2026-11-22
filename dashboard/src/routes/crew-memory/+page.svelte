<script lang="ts">
	import { resolve } from '$app/paths';
	import { crewDirectory, namespace } from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry } from '#lib/crew-display-name.js';
	import { PageTitle } from '#lib/components/common/index.js';
	import { formatTimestamp } from '#lib/discussion-timing.js';
	import MemoryExport from '#lib/components/memory/MemoryExport.svelte';

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

	// Export: the operator builds the OMPI export; the control downloads it.
	const exportEndpoint = resolve('/api/kubemoot/crew-memory/export');
</script>

<div class="page">
	<div class="page-header">
		<PageTitle route="/crew-memory" />
		{#if scope}
			<div class="header-actions">
				<MemoryExport {scope} endpoint={exportEndpoint} hasFacts={facts.length > 0} />
			</div>
		{/if}
	</div>

	{#if error}<p class="error" role="alert">{error}</p>{/if}

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
								<td class="ts">{formatTimestamp(f.usedAt) || f.usedAt}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	{/if}
</div>

<style>
	.page { padding: 1.5rem; max-width: 1100px; }
	.header-actions { display: flex; gap: 0.5rem; }
	.page-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
	.card { background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.5rem; overflow-x: auto; }
	table { width: 100%; border-collapse: collapse; font-size: 0.9rem; color: var(--color-text); }
	th, td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid var(--color-border); vertical-align: top; }
	th { color: var(--color-text-muted); font-weight: 600; font-size: 0.8rem; }
	tbody tr:last-child td { border-bottom: none; }
	.val { max-width: 420px; word-break: break-word; }
	.ts { white-space: nowrap; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
	.empty-state { color: var(--color-text-muted); text-align: center; padding: 2rem 1rem; }
	.error { color: var(--color-error); }
</style>
