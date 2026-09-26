<script lang="ts">
	import type { Snippet } from 'svelte';
	import { HelpTooltip } from '$components/common';

	interface Props {
		title: string;
		count?: number;
		loading?: boolean;
		error?: string | null;
		helpText?: string;
		children: Snippet;
		empty?: Snippet;
		actions?: Snippet;
	}

	let { title, count, loading = false, error = null, helpText, children, empty, actions }: Props = $props();
</script>

<div class="resource-list">
	<header class="header">
		<div class="title-row">
			<h2 class="title">{title}</h2>
			{#if helpText}
				<HelpTooltip text={helpText} />
			{/if}
			{#if count !== undefined}
				<span class="count">{count}</span>
			{/if}
		</div>
		{#if actions}
			<div class="actions">
				{@render actions()}
			</div>
		{/if}
	</header>

	<div class="content">
		{#if loading}
			<div class="loading">
				<div class="spinner"></div>
				<span>Loading...</span>
			</div>
		{:else if error}
			<div class="error">
				<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<circle cx="12" cy="12" r="10" />
					<line x1="12" y1="8" x2="12" y2="12" />
					<line x1="12" y1="16" x2="12.01" y2="16" />
				</svg>
				<span>{error}</span>
			</div>
		{:else if count === 0 && empty}
			<div class="empty">
				{@render empty()}
			</div>
		{:else}
			<div class="grid">
				{@render children()}
			</div>
		{/if}
	</div>
</div>

<style>
	.resource-list {
		display: flex;
		flex-direction: column;
		height: 100%;
	}

	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1.5rem;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.title {
		font-size: 1.5rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.count {
		background-color: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		padding: 0.25rem 0.625rem;
		border-radius: 999px;
		font-size: 0.8rem;
		font-weight: 500;
	}

	.actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.content {
		flex: 1;
		overflow-y: auto;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
		gap: 1rem;
	}

	.loading,
	.error,
	.empty {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 1rem;
		padding: 4rem 2rem;
		color: var(--color-text-muted);
		text-align: center;
	}

	.error {
		color: var(--color-error);
	}

	.spinner {
		width: 32px;
		height: 32px;
		border: 3px solid var(--color-bg-tertiary);
		border-top-color: var(--color-primary);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
