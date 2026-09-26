<script lang="ts">
	let { text }: { text: string } = $props();
	let open = $state(false);

	function handleClickOutside(event: MouseEvent) {
		const target = event.target as HTMLElement;
		if (!target.closest('.help-wrapper')) {
			open = false;
		}
	}
</script>

<svelte:document onclick={handleClickOutside} />

<span class="help-wrapper">
	<button class="help-btn" onclick={(e) => { e.stopPropagation(); open = !open; }} title="Help">?</button>
	{#if open}
		<div class="help-popup" onclick={(e) => e.stopPropagation()}>
			<p>{text}</p>
		</div>
	{/if}
</span>

<style>
	.help-wrapper {
		position: relative;
		display: inline-flex;
		align-items: center;
	}

	.help-btn {
		width: 18px;
		height: 18px;
		border-radius: 50%;
		border: 1px solid var(--color-text-muted);
		background: transparent;
		color: var(--color-text-muted);
		font-size: 0.65rem;
		font-weight: 700;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0;
		line-height: 1;
		transition: all 0.15s;
	}

	.help-btn:hover {
		border-color: var(--color-primary);
		color: var(--color-primary);
	}

	.help-popup {
		position: absolute;
		top: calc(100% + 8px);
		left: 50%;
		transform: translateX(-50%);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		padding: 0.75rem;
		min-width: 260px;
		max-width: 360px;
		z-index: 100;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
	}

	.help-popup::before {
		content: '';
		position: absolute;
		top: -5px;
		left: 50%;
		transform: translateX(-50%) rotate(45deg);
		width: 8px;
		height: 8px;
		background: var(--color-bg-secondary);
		border-left: 1px solid var(--color-border);
		border-top: 1px solid var(--color-border);
	}

	.help-popup p {
		margin: 0;
		font-size: 0.8rem;
		line-height: 1.5;
		color: var(--color-text);
	}
</style>
