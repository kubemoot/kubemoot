<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { get } from 'svelte/store';
	import { namespace, readOnly } from '#lib/stores/index.js';

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

	// Memory keys are <namespace>.<crew>.<topic>.<key>. The namespace field starts
	// at the crew selected in the top bar; left empty, the list shows the crew name
	// in every namespace, and writes need a namespace.
	let ns = $state(get(namespace));
	let crew = $state('homelab-pilot');
	let facts = $state<Fact[]>([]);
	let loading = $state(false);
	let error = $state<string | null>(null);

	// Add / edit form
	let formTopic = $state('');
	let formKey = $state('');
	let formValue = $state('');

	async function load() {
		loading = true;
		error = null;
		try {
			const q = new URLSearchParams({ crew });
			if (ns) q.set('namespace', ns);
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

	async function save() {
		if (!formTopic || !formKey || !formValue || !ns) return;
		try {
			const res = await fetch(resolve('/api/kubemoot/crew-memory'), {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ namespace: ns, crew, topic: formTopic, key: formKey, value: formValue })
			});
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			formTopic = formKey = formValue = '';
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save fact';
		}
	}

	function edit(f: Fact) {
		ns = f.namespace;
		formTopic = f.topic;
		formKey = f.key;
		formValue = f.value;
	}

	async function remove(f: Fact) {
		if (!confirm(`Delete crew-memory fact?\n\n${f.namespace}: [${f.topic}] ${f.key} = ${f.value}`)) return;
		try {
			const q = new URLSearchParams({ namespace: f.namespace, crew: f.crew, topic: f.topic, key: f.key });
			const res = await fetch(`${resolve('/api/kubemoot/crew-memory')}?${q}`, {
				method: 'DELETE',
				headers: { 'Content-Type': 'application/json' }
			});
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to delete fact';
		}
	}

	async function clearAll() {
		if (!ns) return;
		if (!confirm(`Clear ALL working memory for crew "${crew}" in namespace "${ns}"?\n\nThis deletes its fact(s) and cannot be undone.`)) return;
		try {
			const q = new URLSearchParams({ namespace: ns, crew });
			const res = await fetch(`${resolve('/api/kubemoot/crew-memory')}?${q}`, {
				method: 'DELETE',
				headers: { 'Content-Type': 'application/json' }
			});
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to clear memory';
		}
	}

	onMount(load);
</script>

<div class="page">
	<h1>Crew Working Memory</h1>
	<p class="sub">Facts the crew learned on this cluster (NATS KV: <code>kubemoot_crew_memory</code>). Scoped by namespace and crew, GC'd by LRU + TTL.</p>

	<div class="bar">
		<label>Namespace <input bind:value={ns} placeholder="all namespaces" onkeydown={(e) => e.key === 'Enter' && load()} /></label>
		<label>Crew <input bind:value={crew} onkeydown={(e) => e.key === 'Enter' && load()} /></label>
		<button onclick={load} disabled={loading}>{loading ? 'Loading…' : 'Load'}</button>
		{#if !$readOnly}
			<button class="danger" onclick={clearAll} disabled={loading || facts.length === 0 || !ns} title={ns ? undefined : 'Set a namespace to clear one crew'}>Clear all ({facts.length})</button>
		{/if}
	</div>

	{#if error}<p class="error">{error}</p>{/if}

	<table>
		<thead>
			<tr><th>Namespace</th><th>Topic</th><th>Key</th><th>Value</th><th>Learned by</th><th>Used</th><th></th></tr>
		</thead>
		<tbody>
			{#each facts as f (f.namespace + '.' + f.topic + '.' + f.key)}
				<tr>
					<td>{f.namespace}</td>
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
			{#if facts.length === 0 && !loading}
				<tr><td colspan="7" class="empty">No facts learned yet for this crew.</td></tr>
			{/if}
		</tbody>
	</table>

	{#if !$readOnly}
	<h2>Add / update a fact</h2>
	<div class="form">
		<input placeholder="topic (e.g. gpu-topology)" bind:value={formTopic} />
		<input placeholder="key (e.g. rig0)" bind:value={formKey} />
		<input placeholder="value" bind:value={formValue} />
		<button onclick={save} disabled={!formTopic || !formKey || !formValue || !ns}>Save</button>
	</div>
	<p class="hint">Saving writes to the namespace above (required). Saving an existing topic+key updates it (the crew's own discoveries overwrite the same key).</p>
	{/if}
</div>

<style>
	.page { padding: 1.5rem; max-width: 1100px; }
	.sub { color: var(--text-muted, #888); margin-top: -0.5rem; }
	.bar { display: flex; gap: 0.75rem; align-items: center; margin: 1rem 0; }
	.bar input { padding: 0.3rem 0.5rem; }
	table { width: 100%; border-collapse: collapse; font-size: 0.9rem; }
	th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--border, #333); vertical-align: top; }
	.val { max-width: 420px; word-break: break-word; }
	.ts { white-space: nowrap; color: var(--text-muted, #888); font-variant-numeric: tabular-nums; }
	.actions { white-space: nowrap; }
	.empty { color: var(--text-muted, #888); text-align: center; padding: 1rem; }
	.form { display: flex; gap: 0.5rem; flex-wrap: wrap; }
	.form input { padding: 0.3rem 0.5rem; flex: 1; min-width: 160px; }
	.hint { color: var(--text-muted, #888); font-size: 0.85rem; }
	button { padding: 0.3rem 0.7rem; cursor: pointer; }
	button.danger { color: #b00; }
	.error { color: #b00; }
</style>
