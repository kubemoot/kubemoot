<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	interface Props {
		/** What the command does, one short phrase. */
		label: string;
		/** The command to run, shown as copyable text. */
		command: string;
	}

	let { label, command }: Props = $props();

	let copied = $state(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(command);
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			copied = false;
		}
	}
</script>

<div class="command-hint">
	<span class="label">{label}</span>
	<code>{command}</code>
	<button use:tooltip={ACTIONS.copyCommand} type="button" class="copy" onclick={copy} aria-label="Copy the command to the clipboard">
		{copied ? 'Copied' : 'Copy'}
	</button>
</div>

<style>
	.command-hint {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		padding: 0.4rem 0.6rem;
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
		background-color: var(--color-bg-secondary);
		font-size: 0.8rem;
	}

	.label {
		color: var(--color-text-muted, var(--color-text));
	}

	code {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		user-select: all;
		overflow-wrap: anywhere;
	}

	.copy {
		padding: 0.15rem 0.6rem;
		font-size: 0.75rem;
		border-radius: 0.25rem;
		border: 1px solid var(--color-border);
		background-color: var(--color-bg-tertiary);
		color: var(--color-text);
		cursor: pointer;
	}

	.copy:hover {
		background-color: var(--color-bg-secondary);
	}
</style>
