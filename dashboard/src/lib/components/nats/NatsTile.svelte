<script lang="ts">
	import StatusBadge from '#lib/components/common/StatusBadge.svelte';
	import LoadingSpinner from '#lib/components/common/LoadingSpinner.svelte';
	import { formatBytes, formatPercent, tileOf, type NatsStatus, type TileState } from '#lib/nats/status.js';
	import type { BadgeStatus } from '#lib/status.js';

	interface Props {
		/** null until the first read lands. */
		status: NatsStatus | null;
		href: string;
	}

	let { status, href }: Props = $props();

	const tile = $derived(tileOf(status));

	const BADGES: Record<TileState, { status: BadgeStatus; label: string }> = {
		connected: { status: 'success', label: 'Connected' },
		'near-limit': { status: 'warning', label: 'Near a limit' },
		disconnected: { status: 'error', label: 'Not connected' }
	};

	const SHOWN_WARNINGS = 3;

	function usage(u: { used: number; limit: number | null; fraction: number | null }): string {
		return u.limit === null
			? `${formatBytes(u.used)} (no limit)`
			: `${formatBytes(u.used)} of ${formatBytes(u.limit)} (${formatPercent(u.fraction)})`;
	}
</script>

<a {href} class="nats-tile" data-testid="nats-tile">
	<div class="head">
		<span class="label">NATS</span>
		{#if tile}
			<StatusBadge status={BADGES[tile.state].status} label={BADGES[tile.state].label} size="sm" />
		{/if}
	</div>

	{#if !tile}
		<div class="loading" role="status" aria-label="Loading NATS"><LoadingSpinner size="sm" /></div>
	{:else if tile.state === 'disconnected'}
		<div class="detail">{tile.error ?? 'NATS is not reachable'}</div>
	{:else}
		<div class="version">Server {tile.version}</div>
		<dl class="figures">
			{#if tile.storage}
				<dt>File storage</dt>
				<dd data-testid="nats-storage">{usage(tile.storage)}</dd>
			{/if}
			{#if tile.memory}
				<dt>Memory</dt>
				<dd data-testid="nats-memory">{usage(tile.memory)}</dd>
			{/if}
			<dt>{tile.scoped ? 'Your namespaces' : 'Stored, cluster-wide'}</dt>
			<dd data-testid="nats-totals">{tile.messages.toLocaleString('en-US')} messages, {tile.scoped ? 'about ' : ''}{formatBytes(tile.bytes)} in {tile.streams} stores</dd>
		</dl>
		{#if tile.warnings.length > 0}
			<ul class="warnings" data-testid="nats-warnings">
				{#each tile.warnings.slice(0, SHOWN_WARNINGS) as w (w.store + w.kind)}
					<li class={w.level}>{w.store}: {formatPercent(w.fraction)} of its {w.kind} limit</li>
				{/each}
				{#if tile.warnings.length > SHOWN_WARNINGS}
					<li>and {tile.warnings.length - SHOWN_WARNINGS} more</li>
				{/if}
			</ul>
		{/if}
	{/if}
</a>

<style>
	.nats-tile {
		grid-column: span 2;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1.25rem;
		text-decoration: none;
		color: inherit;
		transition: all 0.15s;
	}
	.nats-tile:hover {
		border-color: var(--color-primary);
		text-decoration: none;
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 0.5rem;
	}
	.label {
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--color-text-muted);
	}
	.version,
	.detail {
		font-size: 0.8rem;
		color: var(--color-text-muted);
		margin-bottom: 0.5rem;
	}
	.figures {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.2rem 0.75rem;
		margin: 0;
		font-size: 0.8rem;
	}
	.figures dt {
		color: var(--color-text-muted);
	}
	.figures dd {
		margin: 0;
		color: var(--color-text);
	}
	.warnings {
		margin: 0.75rem 0 0;
		padding: 0.5rem 0.75rem;
		list-style: none;
		border: 1px solid var(--color-warning, #f59e0b);
		border-radius: 0.375rem;
		background: rgba(245, 158, 11, 0.12);
		font-size: 0.75rem;
	}
	.warnings li.critical {
		color: var(--color-error);
		font-weight: 600;
	}
	.loading {
		min-height: 2.5rem;
		display: flex;
		align-items: center;
	}
</style>
