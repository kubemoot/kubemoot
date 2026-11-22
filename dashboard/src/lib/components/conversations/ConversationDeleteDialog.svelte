<script lang="ts">
	import ConfirmDialog from '#lib/components/common/ConfirmDialog.svelte';
	import type { CrewScope } from '#lib/crewScope.js';
	import { AskRefused } from '#lib/ask-client.js';
	import { MAX_REASON_LENGTH, validReason } from '#lib/access-reason.js';
	import { failedStores, removedLines } from '#lib/conversation-actions.js';
	import { ErasureIncomplete, deleteConversation, isStillRunning, needsReason, type ErasureReport } from '#lib/conversation-actions-client.js';

	interface Props {
		scope: CrewScope;
		conversationId: string;
		/** The conversation's first question, to say which one goes. */
		title: string;
		/** The person changed their mind. */
		onclose: () => void;
		/** The conversation is gone from the crew; the page drops it and may show the report. */
		ondeleted: (report: ErasureReport) => void;
		/** Replaceable so a test can answer without a server. */
		fetchFn?: typeof fetch;
	}

	// The delete of one conversation from a list row: ask first, then, as the person, delete it; a
	// running conversation is stopped first when they agree, and a conversation of someone else
	// needs a reason, which the crew records.
	let { scope, conversationId, title, onclose, ondeleted, fetchFn }: Props = $props();

	let busy = $state(false);
	let refusal = $state<string | null>(null);
	let stopFirst = $state(false);
	let reasonNeeded = $state(false);
	let reasonText = $state('');

	const reason = $derived(validReason(reasonText));

	function explain(err: unknown): string | null {
		if (err instanceof ErasureIncomplete) {
			const removed = removedLines(err.report);
			const soFar = removed.length > 0 ? ` Removed so far: ${removed.join(', ')}.` : '';
			return `Not everything was removed: ${failedStores(err.report).join(', ')} could not be cleared. Delete again to remove the rest.${soFar}`;
		}
		return err instanceof AskRefused ? err.message : 'The delete failed. Try again.';
	}

	async function confirm() {
		if (busy) return;
		busy = true;
		refusal = null;
		try {
			ondeleted(await deleteConversation(fetchFn ?? fetch, scope, conversationId, { force: stopFirst, reason: reasonNeeded ? reason : undefined }));
		} catch (err) {
			if (needsReason(err)) reasonNeeded = true;
			else if (!stopFirst && isStillRunning(err)) stopFirst = true;
			else refusal = explain(err);
		} finally {
			busy = false;
		}
	}
</script>

<ConfirmDialog
	question={stopFirst ? 'This conversation is still running. Stop it and delete it?' : 'Delete this conversation?'}
	goes={[`The conversation "${title}"`, 'Its questions, answers and the agents\' messages, from every store that holds them']}
	{refusal}
	{busy}
	canConfirm={!reasonNeeded || reason !== undefined}
	confirmLabel={stopFirst ? 'Stop and delete' : 'Delete'}
	onconfirm={() => void confirm()}
	{onclose}
>
	{#snippet extra()}
		{#if reasonNeeded}
			<label class="reason" for="delete-reason">This is not your conversation. Say why you are deleting it (it is recorded).</label>
			<input id="delete-reason" type="text" maxlength={MAX_REASON_LENGTH} bind:value={reasonText} placeholder="Reason, 1 to {MAX_REASON_LENGTH} characters" onkeydown={(e) => e.key === 'Enter' && reason !== undefined && void confirm()} />
		{/if}
	{/snippet}
</ConfirmDialog>

<style>
	.reason { display: block; margin-bottom: 0.3rem; font-size: 0.8rem; }
	input {
		width: 100%; box-sizing: border-box; margin-bottom: 0.75rem; padding: 0.3rem 0.5rem; border-radius: 0.3rem;
		border: 1px solid var(--color-border); background: var(--color-bg); color: var(--color-text); font: inherit; font-size: 0.85rem;
	}
</style>
