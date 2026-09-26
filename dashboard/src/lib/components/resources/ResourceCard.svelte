<script lang="ts">
	import type { Snippet } from 'svelte';
	import StatusBadge from '../common/StatusBadge.svelte';

	type Status = 'success' | 'warning' | 'error' | 'pending' | 'unknown' | 'hot';

	interface Props {
		name: string;
		kind: string;
		href: string;
		status: Status;
		statusLabel?: string;
		namespace?: string;
		children?: Snippet;
		footer?: Snippet;
	}

	let { name, kind, href, status, statusLabel, namespace, children, footer }: Props = $props();
</script>

<a {href} class="card">
	<header class="header">
		<div class="title-section">
			<div class="kind-row">
				<span class="kind">{kind}</span>
				{#if namespace}
					<span class="namespace-badge">{namespace}</span>
				{/if}
			</div>
			<h3 class="name">{name}</h3>
		</div>
		<StatusBadge {status} label={statusLabel} size="sm" />
	</header>

	{#if children}
		<div class="body">
			{@render children()}
		</div>
	{/if}

	{#if footer}
		<footer class="footer">
			{@render footer()}
		</footer>
	{/if}
</a>

<style>
	.card {
		display: flex;
		flex-direction: column;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 1rem;
		transition: all 0.15s;
		text-decoration: none;
		color: inherit;
	}

	.card:hover {
		border-color: var(--color-primary);
		background-color: rgba(30, 41, 59, 0.8);
		text-decoration: none;
	}

	.header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.title-section {
		min-width: 0;
		flex: 1;
	}

	.kind-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.kind {
		font-size: 0.7rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.namespace-badge {
		font-size: 0.65rem;
		font-weight: 500;
		color: var(--color-primary);
		background-color: rgba(59, 130, 246, 0.15);
		padding: 0.125rem 0.375rem;
		border-radius: 0.25rem;
	}

	.name {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text);
		margin-top: 0.125rem;
		word-break: break-word;
	}

	.body {
		margin-top: 0.875rem;
		padding-top: 0.875rem;
		border-top: 1px solid var(--color-border);
	}

	.footer {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
</style>
