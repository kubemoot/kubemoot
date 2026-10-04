<script lang="ts">
	import { refresh } from '#lib/stores/index.js';

	interface Props {
		loading?: boolean;
	}

	let { loading = false }: Props = $props();

	function handleClick() {
		refresh.trigger();
	}
</script>

<button
	class="refresh-button"
	class:loading
	onclick={handleClick}
	disabled={loading}
	title="Refresh data"
>
	<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<polyline points="23 4 23 10 17 10" />
		<polyline points="1 20 1 14 7 14" />
		<path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15" />
	</svg>
</button>

<style>
	.refresh-button {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		background-color: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
		color: var(--color-text-muted);
		transition: all 0.15s;
	}

	.refresh-button:hover:not(:disabled) {
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		border-color: var(--color-primary);
	}

	.refresh-button:disabled {
		cursor: not-allowed;
		opacity: 0.5;
	}

	.refresh-button.loading svg {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
