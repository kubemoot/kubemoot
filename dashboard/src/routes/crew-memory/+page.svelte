<script lang="ts">
	import { resolve } from '$app/paths';
	import { crewDirectory, namespace, readOnly } from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry, crewLabel } from '#lib/crew-display-name.js';
	import { canSaveFact, factFormProblem, trimmedDraft } from '#lib/crew-memory-form.js';

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

	let formTopic = $state('');
	let formKey = $state('');
	let formValue = $state('');
	const draft = $derived({ topic: formTopic, key: formKey, value: formValue });
	const problem = $derived(factFormProblem(scope, draft));

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

	async function send(method: string, url: string, body?: unknown) {
		const res = await fetch(url, {
			method,
			headers: { 'Content-Type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body)
		});
		const data = await res.json();
		if (data.error) throw new Error(data.error);
	}

	async function save() {
		if (!scope || !canSaveFact(scope, draft)) return;
		try {
			await send('POST', resolve('/api/kubemoot/crew-memory'), { ...scope, ...trimmedDraft(draft) });
			formTopic = formKey = formValue = '';
			await load(scope);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save fact';
		}
	}

	function edit(f: Fact) {
		formTopic = f.topic;
		formKey = f.key;
		formValue = f.value;
	}

	async function remove(f: Fact) {
		if (!confirm(`Delete this fact?\n\n[${f.topic}] ${f.key} = ${f.value}`)) return;
		try {
			const q = new URLSearchParams({ namespace: f.namespace, crew: f.crew, topic: f.topic, key: f.key });
			await send('DELETE', `${resolve('/api/kubemoot/crew-memory')}?${q}`);
			await load(scope);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to delete fact';
		}
	}

	async function clearAll() {
		if (!scope) return;
		if (!confirm(`Clear ALL working memory for ${crewLabel($crewDirectory, scope.namespace, scope.crew)}?\n\nThis deletes its ${facts.length} fact(s) and cannot be undone.`)) return;
		try {
			const q = new URLSearchParams({ namespace: scope.namespace, crew: scope.crew });
			await send('DELETE', `${resolve('/api/kubemoot/crew-memory')}?${q}`);
			await load(scope);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to clear memory';
		}
	}
</script>

<div class="page">
	<div class="page-header">
		<h1>Crew Memory</h1>
		{#if scope && !$readOnly}
			<button class="danger" onclick={clearAll} disabled={loading || facts.length === 0} title="Delete every fact this crew remembers">Clear all ({facts.length})</button>
		{/if}
	</div>
	<p class="sub">
		Working memory is what a crew has learned about this cluster and keeps between discussions, such as which accelerator
		holds which model. Facts are scoped to one crew, and the least recently used are dropped over time.
	</p>

	{#if error}<p class="error" role="alert">{error}</p>{/if}

	{#if !scope}
		<div class="empty-state">Pick one crew in the top bar to see and edit what it remembers.</div>
	{:else}
		<div class="card">
			{#if loading && facts.length === 0}
				<div class="empty-state">Loading...</div>
			{:else if facts.length === 0}
				<div class="empty-state">This crew has not learned any facts yet. Facts appear here as its agents discover things, and you can add one below.</div>
			{:else}
				<table>
					<thead>
						<tr><th>Topic</th><th>Key</th><th>Value</th><th>Learned by</th><th>Last used</th><th></th></tr>
					</thead>
					<tbody>
						{#each facts as f (f.namespace + '.' + f.topic + '.' + f.key)}
							<tr>
								<td>{f.topic}</td>
								<td>{f.key}</td>
								<td class="val">{f.value}</td>
								<td>{f.learnedBy}</td>
								<td class="ts">{f.usedAt}</td>
								<td class="actions">
									{#if !$readOnly}
										<button onclick={() => edit(f)}>Edit</button>
										<button class="danger" onclick={() => remove(f)}>Delete</button>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>

		{#if !$readOnly}
			<h2>Add or update a fact</h2>
			<div class="form">
				<input placeholder="topic (e.g. gpu-topology)" aria-label="Topic" bind:value={formTopic} />
				<input placeholder="key (e.g. rig0)" aria-label="Key" bind:value={formKey} />
				<input placeholder="value" aria-label="Value" bind:value={formValue} />
				<button class="primary" onclick={save} disabled={!!problem} title={problem ?? 'Save this fact for the selected crew'}>Save</button>
			</div>
			<p class="hint">{problem ?? 'Saving an existing topic and key updates that fact.'}</p>
		{/if}
	{/if}
</div>

<style>
	.page { padding: 1.5rem; max-width: 1100px; }
	.page-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
	h1, h2 { color: var(--color-text); }
	h2 { font-size: 1rem; margin-top: 1.5rem; }
	.sub { color: var(--color-text-muted); margin-top: 0.25rem; max-width: 70ch; }
	.card { background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.5rem; overflow-x: auto; }
	table { width: 100%; border-collapse: collapse; font-size: 0.9rem; color: var(--color-text); }
	th, td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid var(--color-border); vertical-align: top; }
	th { color: var(--color-text-muted); font-weight: 600; font-size: 0.8rem; }
	tbody tr:last-child td { border-bottom: none; }
	.val { max-width: 420px; word-break: break-word; }
	.ts { white-space: nowrap; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
	.actions { white-space: nowrap; }
	.empty-state { color: var(--color-text-muted); text-align: center; padding: 2rem 1rem; }
	.form { display: flex; gap: 0.5rem; flex-wrap: wrap; }
	.form input {
		flex: 1; min-width: 160px; padding: 0.4rem 0.6rem;
		background: var(--color-bg-secondary); color: var(--color-text);
		border: 1px solid var(--color-border); border-radius: 0.375rem;
	}
	.hint { color: var(--color-text-muted); font-size: 0.85rem; }
	button {
		padding: 0.3rem 0.7rem; cursor: pointer; font-size: 0.8rem;
		background: var(--color-bg-tertiary); color: var(--color-text);
		border: 1px solid var(--color-border); border-radius: 0.375rem;
	}
	button:disabled { opacity: 0.5; cursor: not-allowed; }
	button.primary { background: var(--color-primary); border-color: var(--color-primary); }
	button.primary:hover:not(:disabled) { background: var(--color-primary-hover); }
	button.danger { color: var(--color-error); }
	.error { color: var(--color-error); }
</style>
