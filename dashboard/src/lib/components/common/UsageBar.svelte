<script lang="ts">
	interface Props {
		/** What is measured: "CPU", "Memory". */
		label: string;
		/** The measurement in words: "6.1 of 16 GiB". */
		text: string;
		/** Share in use, 0 to 1. */
		fraction: number;
		level?: 'normal' | 'warning' | 'critical';
	}

	let { label, text, fraction, level = 'normal' }: Props = $props();

	const percent = $derived(Math.round(Math.min(1, Math.max(0, fraction)) * 100));
</script>

<div class="usage">
	<div class="row">
		<span class="label">{label}</span>
		<span class="percent {level}">{percent}%</span>
	</div>
	<div class="track" role="meter" aria-label="{label} in use" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-valuetext="{percent}% ({text})">
		<div class="fill {level}" style="width: {percent}%"></div>
	</div>
	<div class="text">{text}</div>
</div>

<style>
	.usage {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}
	.row {
		display: flex;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}
	.text {
		font-size: 0.68rem;
		color: var(--color-text-muted);
	}
	.percent {
		font-weight: 600;
		color: var(--color-text);
	}
	.percent.warning {
		color: var(--color-warning);
	}
	.percent.critical {
		color: var(--color-error);
	}
	.track {
		height: 4px;
		border-radius: 2px;
		background-color: var(--color-bg-tertiary);
		overflow: hidden;
	}
	.fill {
		height: 100%;
		border-radius: 2px;
		background-color: var(--color-primary);
	}
	.fill.warning {
		background-color: var(--color-warning);
	}
	.fill.critical {
		background-color: var(--color-error);
	}
</style>
