<script lang="ts">
	import { marked } from 'marked';
	import type { CrewScope } from '#lib/crewScope.js';
	import { AskRefused, WORKING, askCrew, followAnswer, type AnswerState } from '#lib/ask-client.js';

	interface Props {
		/** The one selected crew; null when the selector shows all crews. */
		scope: CrewScope | null;
		/** The open conversation, when one is open: a reply continues it. */
		conversationId?: string;
		/** Called with the conversation id once the crew has taken a question. */
		onAsked?: (conversationId: string) => void;
		/** Show the answer as it streams here; the page turns it off once the discussion is open. */
		showLive?: boolean;
	}

	let { scope, conversationId, onAsked, showLive = true }: Props = $props();

	let text = $state('');
	let sending = $state(false);
	let startNew = $state(false);
	let refusal = $state<{ status: number; message: string } | null>(null);
	let asked = $state<{ question: string; answer: AnswerState } | null>(null);
	let stream: AbortController | null = null;

	const replying = $derived(!!conversationId && !startNew);
	const idleLabel = $derived(replying ? 'Reply' : 'Ask');
	const sendLabel = $derived(sending ? 'Sending...' : idleLabel);

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
			const result = await askCrew(fetch, scope, question, replying ? conversationId : undefined);
			text = '';
			asked = { question, answer: WORKING };
			onAsked?.(result.conversationId);
			follow(scope, result.conversationId, result.requestedAt, question);
		} catch (err) {
			refuse(err);
		} finally {
			sending = false;
		}
	}

	function follow(crew: CrewScope, id: string, requestedAt: string | undefined, question: string) {
		stream = new AbortController();
		followAnswer(fetch, crew, id, requestedAt, (answer) => (asked = { question, answer }), stream.signal).catch(
			(err: unknown) => {
				if (stream?.signal.aborted) return;
				refuse(err);
			}
		);
	}

	function onKey(event: KeyboardEvent) {
		if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
			event.preventDefault();
			void send();
		}
	}
</script>

<div class="ask">
	{#if asked && showLive}
		<div class="live" aria-live="polite">
			<div class="q">You asked: {asked.question}</div>
			{#if asked.answer.phase === 'working' && !asked.answer.answer}
				<div class="status">
					The crew is working on an answer{asked.answer.agents.length ? ` (${asked.answer.agents.join(', ')})` : ''}...
				</div>
			{/if}
			{#if asked.answer.answer}
				<div class="markdown">{@html marked.parse(asked.answer.answer)}</div>
			{/if}
			{#if asked.answer.phase === 'error'}
				<div class="error" role="alert">{asked.answer.error}</div>
			{/if}
		</div>
	{/if}

	{#if refusal}
		<div class="error" role="alert">{refusal.message}{refusal.status ? ` (${refusal.status})` : ''}</div>
	{/if}

	{#if !scope}
		<p class="hint">Select a crew in the top bar to ask it a question.</p>
	{:else}
		<textarea
			bind:value={text}
			onkeydown={onKey}
			rows="1"
			placeholder={replying ? 'Reply in this conversation...' : 'Ask the crew a question...'}
			aria-label="Your question"
		></textarea>
		<div class="row">
			<button type="button" class="send" onclick={send} disabled={sending || text.trim() === ''}>
				{sendLabel}
			</button>
			{#if conversationId}
				<label class="new"><input type="checkbox" bind:checked={startNew} /> Start a new conversation instead</label>
			{/if}
			<span class="hint">Asked as you. Ctrl+Enter sends.</span>
		</div>
	{/if}
</div>

<style>
	.ask { display: flex; flex-direction: column; gap: 0.5rem; }
	textarea {
		width: 100%; resize: vertical; padding: 0.5rem 0.6rem; border-radius: 0.4rem;
		border: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text);
		font-family: inherit;
	}
	.row { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; }
	.send {
		background: var(--color-primary); color: white; border: none; border-radius: 0.4rem;
		padding: 0.4rem 1rem; font-weight: 600;
	}
	.send:disabled { opacity: 0.5; cursor: default; }
	.hint, .new { font-size: 0.75rem; color: var(--color-text-muted); }
	.live { border: 1px solid var(--color-border); border-radius: 0.4rem; padding: 0.5rem 0.75rem; background: var(--color-bg-secondary); }
	.q { font-size: 0.8rem; color: var(--color-text-muted); margin-bottom: 0.3rem; }
	.status { font-size: 0.85rem; color: var(--color-text-muted); }
	.error { color: var(--color-error); font-size: 0.85rem; }
</style>
