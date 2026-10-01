<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { namespace } from '$stores';
	import type { PromptModule } from '$types/kubemoot.js';
	import { LiveList } from '$lib/client/liveList.svelte';

	const live = new LiveList<PromptModule>('promptmodules');
	onMount(() => {
		live.start($namespace);
		return () => live.stop();
	});
	$effect(() => {
		live.setNamespace($namespace);
	});

	// Local aliases so the template narrows null in {#if err && err.length} etc.
	const err = $derived(live.error);
	const busy = $derived(live.loading);

	const sortedModules = $derived(
		[...live.items].sort((a, b) => {
			const ao = a.spec?.order ?? 999;
			const bo = b.spec?.order ?? 999;
			if (ao !== bo) return ao - bo;
			return a.metadata.name.localeCompare(b.metadata.name);
		})
	);

	function contentLength(m: PromptModule): number {
		return m.spec?.content?.length ?? 0;
	}

	function firstLine(m: PromptModule): string {
		const c = m.spec?.content ?? '';
		const lines = c.split('\n').filter((l) => l.trim().length > 0);
		// Pull the DESCRIPTION line if present - it's the natural one-liner.
		const desc = lines.find((l) => l.trim().startsWith('DESCRIPTION '));
		if (desc) return desc.trim().slice('DESCRIPTION '.length);
		// Otherwise the DEFINE DOMAIN line, or the first non-empty line.
		const domain = lines.find((l) => l.trim().startsWith('DEFINE DOMAIN '));
		if (domain) return domain.trim();
		return lines[0]?.slice(0, 120) ?? '';
	}
</script>

<div class="page-header">
	<h1>Prompt Modules</h1>
	<span class="count">{sortedModules.length}</span>
</div>

<p class="page-help">
	PromptModule CRs are the ADL-formatted prompt fragments assembled into each agent's system prompt.
	Order controls assembly sequence (lower first). Shared modules sit at low orders (10, 20, 25);
	per-agent modules at order 30; coordinator-only modules at 5, 45, 50.
</p>

{#if err && sortedModules.length > 0}
	<p class="error-banner" title={err}>Could not refresh just now - showing last known data. ({err.length > 120 ? err.slice(0, 117) + '…' : err})</p>
{/if}

{#if busy && sortedModules.length === 0}
	<p class="status-msg">Loading prompt modules...</p>
{:else if err && sortedModules.length === 0}
	<p class="status-msg error">{err}</p>
{:else if sortedModules.length === 0}
	<p class="status-msg">No prompt modules found in namespace <code>{$namespace}</code></p>
{:else}
	<table class="modules-table">
		<thead>
			<tr>
				<th class="col-order">Order</th>
				<th>Name</th>
				<th>Description</th>
				<th class="col-namespace">Namespace</th>
				<th class="col-size">Size</th>
			</tr>
		</thead>
		<tbody>
			{#each sortedModules as m (m.metadata.namespace + '/' + m.metadata.name)}
				<tr>
					<td class="col-order mono">{m.spec?.order ?? '-'}</td>
					<td>
						<a
							class="mono name-link"
							href="{resolve('/promptmodules/[name]', { name: m.metadata.name })}?namespace={m.metadata.namespace}"
						>{m.metadata.name}</a>
					</td>
					<td class="desc">{firstLine(m)}</td>
					<td class="col-namespace mono muted">{m.metadata.namespace}</td>
					<td class="col-size mono muted">{contentLength(m).toLocaleString()} chars</td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}

<style>
	.page-header {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}
	.page-header h1 { font-size: 1.5rem; font-weight: 600; }
	.count {
		background: var(--color-bg-tertiary);
		padding: 0.15rem 0.6rem;
		border-radius: 999px;
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}

	.page-help {
		color: var(--color-text-muted);
		font-size: 0.85rem;
		margin-bottom: 1.25rem;
		max-width: 900px;
		line-height: 1.45;
	}

	.status-msg { color: var(--color-text-muted); padding: 2rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }
	.status-msg code { color: var(--color-cyan); font-family: var(--font-mono); }

	.error-banner {
		font-size: 0.8rem;
		color: var(--color-warning, #f59e0b);
		background: rgba(245, 158, 11, 0.08);
		border: 1px solid rgba(245, 158, 11, 0.25);
		padding: 0.5rem 0.75rem;
		border-radius: 0.375rem;
		margin-bottom: 1rem;
	}

	.modules-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.85rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		overflow: hidden;
	}

	.modules-table th {
		text-align: left;
		padding: 0.6rem 0.9rem;
		color: var(--color-text-muted);
		font-weight: 500;
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
	}

	.modules-table td {
		padding: 0.55rem 0.9rem;
		border-bottom: 1px solid var(--color-border);
		vertical-align: top;
	}

	.modules-table tr:last-child td { border-bottom: none; }

	.mono { font-family: var(--font-mono); font-size: 0.8rem; }
	.muted { color: var(--color-text-muted); }

	.name-link {
		color: var(--color-primary);
		text-decoration: none;
	}
	.name-link:hover { text-decoration: underline; }

	.col-order { width: 5rem; }
	.col-namespace { width: 12rem; }
	.col-size { width: 7rem; }

	.desc {
		font-size: 0.8rem;
		color: var(--color-text);
		line-height: 1.4;
	}
</style>
