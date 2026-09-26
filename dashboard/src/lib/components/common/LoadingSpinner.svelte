<script lang="ts">
	interface Props {
		size?: 'sm' | 'md' | 'lg';
		label?: string;
	}

	let { size = 'md', label }: Props = $props();

	const sizes = {
		sm: 16,
		md: 24,
		lg: 32
	};
</script>

<div class="loading-spinner {size}">
	<svg
		width={sizes[size]}
		height={sizes[size]}
		viewBox="0 0 24 24"
		fill="none"
		class="spinner"
	>
		<circle
			cx="12"
			cy="12"
			r="10"
			stroke="currentColor"
			stroke-width="3"
			stroke-linecap="round"
			class="track"
		/>
		<circle
			cx="12"
			cy="12"
			r="10"
			stroke="currentColor"
			stroke-width="3"
			stroke-linecap="round"
			stroke-dasharray="31.4 31.4"
			class="progress"
		/>
	</svg>
	{#if label}
		<span class="label">{label}</span>
	{/if}
</div>

<style>
	.loading-spinner {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--color-text-muted);
	}

	.spinner {
		animation: rotate 1s linear infinite;
	}

	.track {
		opacity: 0.2;
	}

	.progress {
		animation: dash 1.5s ease-in-out infinite;
	}

	.label {
		font-size: 0.875rem;
	}

	.sm .label {
		font-size: 0.75rem;
	}

	.lg .label {
		font-size: 1rem;
	}

	@keyframes rotate {
		to {
			transform: rotate(360deg);
		}
	}

	@keyframes dash {
		0% {
			stroke-dasharray: 1 62.8;
			stroke-dashoffset: 0;
		}
		50% {
			stroke-dasharray: 31.4 31.4;
			stroke-dashoffset: -15.7;
		}
		100% {
			stroke-dasharray: 1 62.8;
			stroke-dashoffset: -62.8;
		}
	}
</style>
