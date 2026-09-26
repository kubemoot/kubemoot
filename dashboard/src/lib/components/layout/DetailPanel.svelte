<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		subtitle?: string;
		loading?: boolean;
		error?: string | null;
		children: Snippet;
		header?: Snippet;
		actions?: Snippet;
	}

	let { title, subtitle, loading = false, error = null, children, header, actions }: Props = $props();
</script>

<div class="detail-panel">
	<header class="panel-header">
		<div class="title-section">
			{#if header}
				{@render header()}
			{:else}
				<h1 class="title">{title ?? 'Unknown'}</h1>
				{#if subtitle}
					<p class="subtitle">{subtitle}</p>
				{/if}
			{/if}
		</div>
		{#if actions}
			<div class="actions">
				{@render actions()}
			</div>
		{/if}
	</header>

	<div class="panel-content">
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
		{:else}
			{@render children()}
		{/if}
	</div>
</div>

<style>
	.detail-panel {
		display: flex;
		flex-direction: column;
		height: 100%;
	}

	.panel-header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		margin-bottom: 2rem;
		padding-bottom: 1.5rem;
		border-bottom: 1px solid var(--color-border);
	}

	.title-section {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.title {
		font-size: 1.75rem;
		font-weight: 600;
		color: var(--color-text);
	}

	.subtitle {
		font-size: 0.9rem;
		color: var(--color-text-muted);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.panel-content {
		flex: 1;
		overflow-y: auto;
	}

	.loading,
	.error {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 1rem;
		padding: 4rem 2rem;
		color: var(--color-text-muted);
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
