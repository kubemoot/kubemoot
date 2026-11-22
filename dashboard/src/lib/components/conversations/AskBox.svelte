<script lang="ts">
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import type { CrewScope } from '#lib/crewScope.js';
	import { AskRefused, WORKING, askCrew, followAnswer, type AnswerState } from '#lib/ask-client.js';

	/** The question just sent and its answer as it streams, for the page to show. */
	export interface Live {
		question: string;
		answer: AnswerState;
	}

	interface Props {
		/** The one selected crew; null when the selector shows all crews. */
		scope: CrewScope | null;
		/** The open conversation, when one is open: the text is a follow-up in it. */
		conversationId?: string;
		/** Called with the conversation id, the question and the time the crew took it, once the crew has taken a question. */
		onAsked?: (conversationId: string, question: string, requestedAt?: string) => void;
		/** The exchange in flight; the page shows it until the conversation itself is open. */
		live?: Live | null;
		/** Put the cursor in the field when it appears, as when a new conversation is started. */
		focus?: boolean;
		/** Replaceable so a test can answer without a server. */
		fetchFn?: typeof fetch;
	}

	let { scope, conversationId, onAsked, live = $bindable(null), focus = false, fetchFn }: Props = $props();

	let text = $state('');
	let sending = $state(false);
	let refusal = $state<{ status: number; message: string } | null>(null);
	let field = $state<HTMLTextAreaElement | null>(null);
	let stream: AbortController | null = null;

	const replying = $derived(!!conversationId);
	const call = (): typeof fetch => fetchFn ?? fetch;

	$effect(() => {
		if (focus && field) field.focus();
	});

	function refuse(err: unknown) {
		if (err instanceof AskRefused) refusal = { status: err.status, message: err.message };
		else refusal = { status: 0, message: err instanceof Error ? err.message : 'Sending failed.' };
	}

	async function send() {
		if (!scope || sending || text.trim() === '') return;
		const question = text.trim();
		sending = true;
		refusal = null;
		stream?.abort();
		try {
			const result = await askCrew(call(), scope, question, replying ? conversationId : undefined);
			text = '';
			live = { question, answer: WORKING };
			onAsked?.(result.conversationId, question, result.requestedAt);
			follow(scope, result.conversationId, result.requestedAt, question);
		} catch (err) {
			refuse(err);
		} finally {
			sending = false;
		}
	}

	function follow(crew: CrewScope, id: string, requestedAt: string | undefined, question: string) {
		stream = new AbortController();
		followAnswer(call(), crew, id, requestedAt, (answer) => (live = { question, answer }), stream.signal).catch(
			(err: unknown) => {
				if (stream?.signal.aborted) return;
				refuse(err);
			}
		);
	}

	function onKey(event: KeyboardEvent) {
		if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
			event.preventDefault();
			void send();
		}
	}
</script>

<div class="ask">
	{#if refusal}
		<div class="error" role="alert">{refusal.message}{refusal.status ? ` (${refusal.status})` : ''}</div>
	{/if}
	{#if !scope}
		<p class="hint">Select a crew in the top bar to ask it a question.</p>
	{:else}
		<div class="compose">
			<textarea
				bind:this={field}
				bind:value={text}
				onkeydown={onKey}
				rows="1"
				placeholder={replying ? 'Reply in this conversation...' : 'Ask the crew a question...'}
				aria-label="Your question"
			></textarea>
			<button type="button" class="send" onclick={send} disabled={sending || text.trim() === ''} use:tooltip={ACTIONS.sendQuestion(replying)} aria-label={replying ? 'Send reply' : 'Ask'}>
				{#if sending}
					<span class="dots">...</span>
				{:else}
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="22" y1="2" x2="11" y2="13" /><polygon points="22 2 15 22 11 13 2 9 22 2" /></svg>
				{/if}
			</button>
		</div>
	{/if}
</div>

<style>
	.ask { display: flex; flex-direction: column; gap: 0.35rem; }
	.compose { display: flex; align-items: flex-end; gap: 0.5rem; }
	textarea {
		flex: 1; min-width: 0; resize: none; field-sizing: content; max-height: 8rem; padding: 0.45rem 0.6rem;
		border-radius: 0.4rem; border: 1px solid var(--color-border); background: var(--color-bg-secondary);
		color: var(--color-text); font-family: inherit; font-size: 0.9rem;
	}
	.send {
		display: inline-flex; align-items: center; justify-content: center; width: 2.2rem; height: 2.2rem;
		background: var(--color-primary); color: white; border: none; border-radius: 0.4rem; cursor: pointer;
	}
	.send:disabled { opacity: 0.5; cursor: default; }
	.hint { font-size: 0.8rem; color: var(--color-text-muted); }
	.error { color: var(--color-error); font-size: 0.85rem; }
</style>
