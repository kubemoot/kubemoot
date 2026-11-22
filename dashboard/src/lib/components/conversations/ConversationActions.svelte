<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import ActionMenu from '#lib/components/common/ActionMenu.svelte';
	import type { CrewScope } from '#lib/crewScope.js';
	import { AskRefused } from '#lib/ask-client.js';
	import { MAX_REASON_LENGTH, validReason } from '#lib/access-reason.js';
	import { conversationMenu } from '#lib/conversation-menu.js';
	import { failedStores, idWithQuestion, removedLines, type RunState } from '#lib/conversation-actions.js';
	import {
		ErasureIncomplete,
		controlConversation,
		deleteConversation,
		isStillRunning,
		needsReason,
		type Control,
		type ErasureReport
	} from '#lib/conversation-actions-client.js';

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

	type Pending = { type: 'control'; control: Control } | { type: 'delete'; force: boolean };
	type Notice =
		| { kind: 'info' | 'error'; text: string }
		| { kind: 'confirm' }
		| { kind: 'running' }
		| { kind: 'reason'; pending: Pending }
		| { kind: 'incomplete'; report: ErasureReport };

	let notice = $state<Notice | null>(null);
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

	function refused(err: unknown, pending: Pending) {
		if (needsReason(err)) notice = { kind: 'reason', pending };
		else if (pending.type === 'delete' && !pending.force && isStillRunning(err)) notice = { kind: 'running' };
		else if (err instanceof ErasureIncomplete) notice = { kind: 'incomplete', report: err.report };
		else say('error', err instanceof AskRefused ? err.message : 'The action failed. Try again.');
	}

	async function perform(pending: Pending, reason?: string) {
		if (!scope || !conversationId || busy) return;
		busy = true;
		try {
			if (pending.type === 'control') {
				await controlConversation(call(), scope, conversationId, pending.control, reason);
				say('info', DONE[pending.control]);
			} else {
				const report = await deleteConversation(call(), scope, conversationId, { force: pending.force, reason });
				notice = null;
				ondeleted(report);
			}
		} catch (err) {
			refused(err, pending);
		} finally {
			busy = false;
		}
	}

	function submitReason() {
		if (notice?.kind !== 'reason' || !reasonValid) return;
		void perform(notice.pending, reasonText);
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
			control: (c) => void perform({ type: 'control', control: c }),
			remove: () => (notice = { kind: 'confirm' })
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
			{:else if notice.kind === 'confirm'}
				<span>Delete this conversation from every store that holds it? This cannot be undone.</span>
				<span class="buttons">
					<button type="button" class="danger" disabled={busy} onclick={() => void perform({ type: 'delete', force: false })}>Yes, delete</button>
					<button type="button" aria-label="No, keep the conversation" onclick={() => (notice = null)}>No</button>
				</span>
			{:else if notice.kind === 'running'}
				<span>This conversation is still running. Stop it, then delete it?</span>
				<span class="buttons">
					<button type="button" class="danger" disabled={busy} onclick={() => void perform({ type: 'delete', force: true })}>Stop and delete</button>
					<button type="button" onclick={() => (notice = null)}>Cancel</button>
				</span>
			{:else if notice.kind === 'reason'}
				<label for="access-reason">This is not your conversation. Say why you are acting on it (it is recorded).</label>
				<input id="access-reason" type="text" maxlength={MAX_REASON_LENGTH} bind:value={reasonText} placeholder="Reason, 1 to {MAX_REASON_LENGTH} characters" onkeydown={(e) => e.key === 'Enter' && submitReason()} />
				<span class="buttons">
					<button type="button" disabled={!reasonValid || busy} onclick={submitReason}>Continue</button>
					<button type="button" onclick={() => (notice = null)}>Cancel</button>
				</span>
			{:else if notice.kind === 'incomplete'}
				<span class="error">Not everything was removed: {failedStores(notice.report).join(', ')} could not be cleared. Repeat the delete to remove the rest.</span>
				<span class="buttons">
					<button type="button" class="danger" disabled={busy} onclick={() => void perform({ type: 'delete', force: false })}>Repeat delete</button>
					<button type="button" onclick={() => (notice = null)}>Close</button>
				</span>
				{#if removedLines(notice.report).length > 0}<span class="muted">Removed so far: {removedLines(notice.report).join(', ')}.</span>{/if}
			{/if}
		</div>
	{/if}
</div>

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
	.notice .danger { border-color: var(--color-error); color: var(--color-error); }
	.notice .plain { align-self: flex-end; border: none; font-size: 1rem; line-height: 1; padding: 0 0.25rem; }
	.notice input {
		padding: 0.3rem 0.5rem; border-radius: 0.3rem; border: 1px solid var(--color-border);
		background: var(--color-bg); color: var(--color-text); font: inherit; font-size: 0.8rem;
	}
	.error { color: var(--color-error); }
	.muted { color: var(--color-text-muted); }
</style>
