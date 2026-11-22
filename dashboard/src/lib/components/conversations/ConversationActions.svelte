<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import ActionMenu from '#lib/components/common/ActionMenu.svelte';
	import type { CrewScope } from '#lib/crewScope.js';
	import { AskRefused } from '#lib/ask-client.js';
	import { MAX_REASON_LENGTH, validReason } from '#lib/access-reason.js';
	import { conversationMenu } from '#lib/conversation-menu.js';
	import { idWithQuestion, type RunState } from '#lib/conversation-actions.js';
	import ConversationDeleteDialog from './ConversationDeleteDialog.svelte';
	import { controlConversation, needsReason, type Control, type ErasureReport } from '#lib/conversation-actions-client.js';

	interface Props {
		scope: CrewScope | null;
		conversationId: string | undefined;
		/** The conversation's first question, for the copy that pairs it with the id. */
		question: string;
		running: RunState;
		/** The conversation as text, made when the person copies it. */
		copyText: () => string;
		showDetails: boolean;
		ontoggledetails: () => void;
		downloadHref: string | null;
		discussionHref: string;
		/** The conversation is gone from the crew; the page drops it and shows the report. */
		ondeleted: (report: ErasureReport) => void;
		/** Replaceable so a test can answer without a server. */
		fetchFn?: typeof fetch;
	}

	let {
		scope, conversationId, question, running, copyText, showDetails, ontoggledetails,
		downloadHref, discussionHref, ondeleted, fetchFn
	}: Props = $props();

	// Stop, pause and resume run here; deleting asks in a dialog of its own, shared with the list rows.
	type Notice =
		| { kind: 'info' | 'error'; text: string }
		| { kind: 'reason'; control: Control };

	let notice = $state<Notice | null>(null);
	let deleting = $state(false);
	let busy = $state(false);
	let reasonText = $state('');
	let timer: ReturnType<typeof setTimeout> | undefined;

	const call = (): typeof fetch => fetchFn ?? fetch;
	const reasonValid = $derived(validReason(reasonText) !== undefined);

	function say(kind: 'info' | 'error', text: string) {
		notice = { kind, text };
		clearTimeout(timer);
		if (kind === 'info') timer = setTimeout(() => (notice = null), 4000);
	}

	async function copy(label: string, text: string) {
		try {
			await navigator.clipboard.writeText(text);
			say('info', `${label} copied.`);
		} catch {
			say('error', 'The browser did not allow copying.');
		}
	}

	const DONE: Record<Control, string> = {
		stop: 'Stop requested. The crew stops at its next step.',
		pause: 'Pause requested.',
		resume: 'Resume requested.'
	};

	function refused(err: unknown, control: Control) {
		if (needsReason(err)) notice = { kind: 'reason', control };
		else say('error', err instanceof AskRefused ? err.message : 'The action failed. Try again.');
	}

	async function perform(control: Control, reason?: string) {
		if (!scope || !conversationId || busy) return;
		busy = true;
		try {
			await controlConversation(call(), scope, conversationId, control, reason);
			say('info', DONE[control]);
		} catch (err) {
			refused(err, control);
		} finally {
			busy = false;
		}
	}

	function submitReason() {
		if (notice?.kind !== 'reason' || !reasonValid) return;
		void perform(notice.control, reasonText);
	}

	const entries = $derived(
		conversationMenu({
			scope,
			conversationId,
			run: running,
			showDetails,
			downloadHref,
			discussionHref,
			ontoggledetails,
			copyConversation: () => void copy('Conversation', copyText()),
			copyIdAndQuestion: () => void copy('Id and question', idWithQuestion(conversationId ?? '', question)),
			control: (c) => void perform(c),
			remove: () => (deleting = true)
		})
	);
</script>

<div class="actions">
	<ActionMenu {entries} label="Conversation actions" tip={ACTIONS.conversationActions} />
	{#if notice}
		<div class="notice" role={notice.kind === 'error' ? 'alert' : 'status'}>
			{#if notice.kind === 'info' || notice.kind === 'error'}
				<span class:error={notice.kind === 'error'}>{notice.text}</span>
				<button type="button" class="plain" use:tooltip={ACTIONS.dismissNotice} onclick={() => (notice = null)} aria-label="Dismiss">&times;</button>
			{:else if notice.kind === 'reason'}
				<label for="access-reason">This is not your conversation. Say why you are acting on it (it is recorded).</label>
				<input id="access-reason" type="text" maxlength={MAX_REASON_LENGTH} bind:value={reasonText} placeholder="Reason, 1 to {MAX_REASON_LENGTH} characters" onkeydown={(e) => e.key === 'Enter' && submitReason()} />
				<span class="buttons">
					<button type="button" disabled={!reasonValid || busy} onclick={submitReason}>Continue</button>
					<button type="button" onclick={() => (notice = null)}>Cancel</button>
				</span>
			{/if}
		</div>
	{/if}
</div>

{#if deleting && scope && conversationId}
	<ConversationDeleteDialog
		{scope}
		{conversationId}
		title={question}
		onclose={() => (deleting = false)}
		ondeleted={(report: ErasureReport) => {
			deleting = false;
			ondeleted(report);
		}}
		{fetchFn}
	/>
{/if}

<style>
	.actions { position: relative; display: inline-flex; }
	.notice {
		position: absolute; right: 0; top: calc(100% + 0.25rem); z-index: 15; width: 22rem; max-width: 80vw;
		display: flex; flex-direction: column; gap: 0.4rem; padding: 0.6rem 0.75rem; font-size: 0.8rem;
		background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: 0.375rem;
		box-shadow: var(--shadow-popup);
	}
	.buttons { display: flex; gap: 0.4rem; }
	.notice button {
		background: none; border: 1px solid var(--color-border); border-radius: 0.3rem; padding: 0.15rem 0.6rem;
		color: var(--color-text); font-size: 0.8rem; cursor: pointer;
	}
	.notice button:disabled { opacity: 0.5; cursor: default; }
	.notice .plain { align-self: flex-end; border: none; font-size: 1rem; line-height: 1; padding: 0 0.25rem; }
	.notice input {
		padding: 0.3rem 0.5rem; border-radius: 0.3rem; border: 1px solid var(--color-border);
		background: var(--color-bg); color: var(--color-text); font: inherit; font-size: 0.8rem;
	}
	.error { color: var(--color-error); }
</style>
