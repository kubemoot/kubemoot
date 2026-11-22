<script lang="ts">
	import { resolve } from '$app/paths';
	import HealthBadge from '#lib/components/common/HealthBadge.svelte';
	import LoadingSpinner from '#lib/components/common/LoadingSpinner.svelte';
	import UsageBar from '#lib/components/common/UsageBar.svelte';
	import { linkHref } from '#lib/overview-link.js';
	import type { Tile } from '#lib/overview-model.js';

	interface Props {
		tile: Tile;
	}

	let { tile }: Props = $props();
</script>

<a href={linkHref(tile.link, resolve('/'))} class="tile" data-testid="tile-{tile.key}">
	<div class="head">
		<span class="label">{tile.label}</span>
		{#if tile.phase !== 'loading'}<HealthBadge health={tile.health} />{/if}
	</div>
	{#if tile.phase === 'loading'}
		<div class="loading" role="status" aria-label="Loading {tile.label}"><LoadingSpinner size="sm" /></div>
	{:else}
		{#if tile.figure}<div class="figure">{tile.figure}</div>{/if}
		{#if tile.line}<div class="line">{tile.line}</div>{/if}
		{#if tile.details && tile.details.length > 0}
			<ul class="details">
				{#each tile.details as fact (fact.text)}<li class:refused={!fact.read}>{fact.text}</li>{/each}
			</ul>
		{/if}
		{#if tile.bars.length > 0}
			<div class="bars">
				{#each tile.bars as b (b.label)}
					<UsageBar label={b.label} text={b.text} fraction={b.fraction} level={b.level} />
				{/each}
			</div>
		{/if}
		{#if tile.note}<div class="note">{tile.note}</div>{/if}
	{/if}
</a>

<style>
	.tile {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1.25rem;
		text-decoration: none;
		color: inherit;
		transition: border-color 0.15s;
	}
	.tile:hover {
		border-color: var(--color-primary);
		text-decoration: none;
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.label {
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--color-text-muted);
	}
	.figure {
		font-size: 2.25rem;
		font-weight: 700;
		line-height: 1;
		color: var(--color-text);
	}
	.line {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
	.details {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
	.details .refused {
		color: var(--color-warning);
	}
	.bars {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-top: 0.25rem;
	}
	.note {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		font-style: italic;
	}
	.loading {
		min-height: 2.5rem;
		display: flex;
		align-items: center;
	}
</style>
