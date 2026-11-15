<script lang="ts">
	import { unsentMessages, retryUnsent, type Unsent } from '#lib/send-state.js';

	interface Props {
		messageId: string;
		/** Sends the message again; the same one the page sends with. */
		publish: (subject: string, message: Unsent['message']) => Promise<void>;
	}

	let { messageId, publish }: Props = $props();
	const entry = $derived($unsentMessages.get(messageId));
	let retrying = $state(false);

	async function retry() {
		retrying = true;
		try {
			await retryUnsent(publish, messageId, $unsentMessages);
		} finally {
			retrying = false;
		}
	}
</script>

{#if entry}
	<div class="not-sent" role="alert">
		<span>Not sent: {entry.reason}</span>
		<button class="retry-send" onclick={retry} disabled={retrying}>{retrying ? 'Sending...' : 'Retry'}</button>
	</div>
{/if}

<style>
	.not-sent {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.35rem;
		font-size: 0.8rem;
		color: var(--color-error);
	}
	.retry-send {
		font-size: 0.75rem;
		padding: 0.1rem 0.5rem;
		border: 1px solid var(--color-error);
		border-radius: 4px;
		background: transparent;
		color: var(--color-error);
		cursor: pointer;
	}
	.retry-send:disabled {
		opacity: 0.6;
		cursor: default;
	}
</style>
