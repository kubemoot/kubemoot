<script lang="ts">
	import { onMount } from 'svelte';
	import { base } from '$app/paths';

	interface Fact {
		topic: string;
		key: string;
		value: string;
		learnedBy: string;
		learnedAt: string;
		usedAt: string;
	}

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
			const res = await fetch(`${base}/api/kubemoot/crew-memory?crew=${encodeURIComponent(crew)}`);
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
		if (!formTopic || !formKey || !formValue) return;
		try {
			const res = await fetch(`${base}/api/kubemoot/crew-memory`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ crew, topic: formTopic, key: formKey, value: formValue })
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
		formTopic = f.topic;
		formKey = f.key;
		formValue = f.value;
	}

	async function remove(f: Fact) {
		if (!confirm(`Delete crew-memory fact?\n\n[${f.topic}] ${f.key} = ${f.value}`)) return;
		try {
			const q = `crew=${encodeURIComponent(crew)}&topic=${encodeURIComponent(f.topic)}&key=${encodeURIComponent(f.key)}`;
			const res = await fetch(`${base}/api/kubemoot/crew-memory?${q}`, { method: 'DELETE' });
			const data = await res.json();
			if (data.error) throw new Error(data.error);
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to delete fact';
		}
	}

	async function clearAll() {
		if (!confirm(`Clear ALL working memory for crew "${crew}"?\n\nThis deletes ${facts.length} fact(s) and cannot be undone.`)) return;
		try {
			const res = await fetch(`${base}/api/kubemoot/crew-memory?crew=${encodeURIComponent(crew)}`, {
				method: 'DELETE'
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
	<p class="sub">Facts the crew learned on this cluster (NATS KV: <code>kubemoot_crew_memory</code>). Crew-scoped, GC'd by LRU + TTL.</p>

	<div class="bar">
		<label>Crew <input bind:value={crew} onkeydown={(e) => e.key === 'Enter' && load()} /></label>
		<button onclick={load} disabled={loading}>{loading ? 'Loading…' : 'Load'}</button>
		<button class="danger" onclick={clearAll} disabled={loading || facts.length === 0}>Clear all ({facts.length})</button>
	</div>

	{#if error}<p class="error">{error}</p>{/if}

	<table>
		<thead>
			<tr><th>Topic</th><th>Key</th><th>Value</th><th>Learned by</th><th>Used</th><th></th></tr>
		</thead>
		<tbody>
			{#each facts as f (f.topic + '.' + f.key)}
				<tr>
					<td>{f.topic}</td>
					<td>{f.key}</td>
					<td class="val">{f.value}</td>
					<td>{f.learnedBy}</td>
					<td class="ts">{f.usedAt}</td>
					<td class="actions">
						<button onclick={() => edit(f)}>Edit</button>
						<button class="danger" onclick={() => remove(f)}>Delete</button>
					</td>
				</tr>
			{/each}
			{#if facts.length === 0 && !loading}
				<tr><td colspan="6" class="empty">No facts learned yet for this crew.</td></tr>
			{/if}
		</tbody>
	</table>

	<h2>Add / update a fact</h2>
	<div class="form">
		<input placeholder="topic (e.g. gpu-topology)" bind:value={formTopic} />
		<input placeholder="key (e.g. rig0)" bind:value={formKey} />
		<input placeholder="value" bind:value={formValue} />
		<button onclick={save} disabled={!formTopic || !formKey || !formValue}>Save</button>
	</div>
	<p class="hint">Saving an existing topic+key updates it (the crew's own discoveries overwrite the same key).</p>
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
