<script lang="ts">
	type Status = 'success' | 'warning' | 'error' | 'pending' | 'unknown' | 'hot';

	interface Props {
		status: Status;
		label?: string;
		size?: 'sm' | 'md';
	}

	let { status, label, size = 'md' }: Props = $props();

	const statusLabels: Record<Status, string> = {
		success: 'Ready',
		warning: 'Warning',
		error: 'Error',
		pending: 'Pending',
		unknown: 'Unknown',
		hot: 'GPU Loaded'
	};

	const displayLabel = $derived(label || statusLabels[status]);
</script>

<span class="badge {status} {size}">
	<span class="dot"></span>
	<span class="label">{displayLabel}</span>
</span>

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.25rem 0.625rem;
		border-radius: 999px;
		font-weight: 500;
	}

	.badge.sm {
		font-size: 0.7rem;
		padding: 0.125rem 0.5rem;
	}

	.badge.md {
		font-size: 0.75rem;
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
	}

	.badge.sm .dot {
		width: 5px;
		height: 5px;
	}

	.success {
		background-color: rgba(34, 197, 94, 0.15);
		color: var(--color-success);
	}

	.success .dot {
		background-color: var(--color-success);
	}

	.warning {
		background-color: rgba(245, 158, 11, 0.15);
		color: var(--color-warning);
	}

	.warning .dot {
		background-color: var(--color-warning);
	}

	.error {
		background-color: rgba(239, 68, 68, 0.15);
		color: var(--color-error);
	}

	.error .dot {
		background-color: var(--color-error);
	}

	.pending {
		background-color: rgba(59, 130, 246, 0.15);
		color: var(--color-primary);
	}

	.pending .dot {
		background-color: var(--color-primary);
		animation: pulse 1.5s ease-in-out infinite;
	}

	.hot {
		background-color: rgba(245, 158, 11, 0.15);
		color: var(--color-warning);
	}

	.hot .dot {
		background-color: var(--color-warning);
		animation: pulse 1.5s ease-in-out infinite;
	}

	.unknown {
		background-color: rgba(148, 163, 184, 0.15);
		color: var(--color-text-muted);
	}

	.unknown .dot {
		background-color: var(--color-text-muted);
	}

	@keyframes pulse {
		0%, 100% {
			opacity: 1;
		}
		50% {
			opacity: 0.4;
		}
	}
</style>
