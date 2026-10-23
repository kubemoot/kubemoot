<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { resolve } from '$app/paths';
	import { marked } from 'marked';
	import { currentRequester } from '#lib/requester.js';
	import type { DiscussionMessage } from '#lib/types/kubemoot.js';
	import {
		crewDirectory,
		threads,
		sortedThreads,
		initDiscussions,
		addDiscussionMessage,
		removeThread,
		showChatDetails,
		namespace,
		threadScope,
		readOnly
	} from '#lib/stores/index.js';
	import { discussSubject, tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry, crewLabel } from '#lib/crew-display-name.js';
	import { canSend, chatAsText, chatTurns, crewThreads, detailsLine } from '#lib/chat-view.js';
	import { formatTimestamp } from '#lib/discussion-timing.js';

	marked.setOptions({ breaks: true, gfm: true });

	// The crew picker is the top-bar "Crew:" selector, the same one Discussions uses.
	const selectedScope = $derived<CrewScope | null>(
		$namespace === '' ? null : tryCrewScope($namespace, crewEntry($crewDirectory, $namespace)?.crew)
	);
	const chats = $derived(
		crewThreads($sortedThreads, selectedScope).filter((t) => chatTurns(t.messages).length > 0)
	);

	let selectedThreadId = $state<string | null>(null);
	const selected = $derived(selectedThreadId ? $threads.get(selectedThreadId) : undefined);
	const turns = $derived(selected ? chatTurns(selected.messages) : []);

	let draft = $state('');
	let sending = $state(false);
	let channel = $state('general');
	let deleteConfirm = $state(false);
	let copied = $state(false);
	let timelineEl = $state<HTMLElement | null>(null);

	const sendable = $derived(
		canSend({ scope: selectedScope, text: draft, sending, readOnly: $readOnly })
	);

	function selectChat(threadId: string | null) {
		selectedThreadId = threadId;
		deleteConfirm = false;
	}

	async function publish(subject: string, msg: DiscussionMessage) {
		const res = await fetch(resolve('/api/nats/publish'), {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ subject, data: JSON.stringify(msg) })
		});
		if (!res.ok) throw new Error('Failed to publish');
	}

	// A new conversation starts a thread (thread_start on the crew's discuss subject, as
	// New discussion does); a message in an open conversation is a reply on its thread.
	function outgoing(scope: CrewScope, text: string): { subject: string; msg: DiscussionMessage } {
		const base = {
			messageId: crypto.randomUUID(),
			agentName: 'human',
			content: text,
			timestamp: new Date().toISOString()
		};
		if (selected) {
			const threadScopeOf = threadScope(selected) ?? scope;
			return {
				subject: discussSubject(threadScopeOf, selected.channel, selected.threadId),
				msg: { ...base, threadId: selected.threadId, messageType: 'reply', channel: selected.channel }
			};
		}
		const threadId = crypto.randomUUID();
		return {
			subject: discussSubject(scope, channel, threadId),
			msg: {
				...base,
				threadId,
				messageType: 'thread_start',
				channel,
				metadata: { userQuery: text, requester: currentRequester() }
			}
		};
	}

	async function send() {
		if (!sendable || !selectedScope) return;
		sending = true;
		try {
			const { subject, msg } = outgoing(selectedScope, draft.trim());
			addDiscussionMessage(msg, subject);
			selectedThreadId = msg.threadId;
			await publish(subject, msg);
			draft = '';
			await tick();
			if (timelineEl) timelineEl.scrollTop = timelineEl.scrollHeight;
		} catch (e) {
			console.error('Failed to send message:', e);
		} finally {
			sending = false;
		}
	}

	async function copyChat() {
		await navigator.clipboard.writeText(chatAsText(turns, $showChatDetails));
		copied = true;
		setTimeout(() => {
			copied = false;
		}, 2000);
	}

	async function deleteChat() {
		if (!selected) return;
		const id = selected.threadId;
		await removeThread(id);
		deleteConfirm = false;
		selectedThreadId = chats.find((t) => t.threadId !== id)?.threadId ?? null;
	}

	function preview(threadId: string, fallback: string): string {
		const first = $threads.get(threadId);
		return chatTurns(first?.messages ?? [])[0]?.text || fallback;
	}

	async function loadChannel() {
		try {
			const res = await fetch(resolve('/api/kubemoot/channels'));
			const data = await res.json();
			if (data.channels?.length > 0) channel = data.channels[0].name;
		} catch {
			// The default channel stays.
		}
	}

	onMount(() => {
		initDiscussions();
		loadChannel();
	});
</script>

<div class="messages-page">
	<div class="sidebar-panel">
		<div class="panel-header">
			<h2>Conversations</h2>
			{#if !$readOnly}
				<button class="new-btn" onclick={() => selectChat(null)} title="Start a new conversation with the selected crew">+ New</button>
			{/if}
		</div>
		{#if chats.length === 0}
			<p class="hint">No conversations{selectedScope ? ' with this crew' : ''} yet.</p>
		{/if}
		{#each chats as t (t.threadId)}
			<button class="chat-row" class:active={t.threadId === selectedThreadId} onclick={() => selectChat(t.threadId)}>
				<span class="chat-preview">{preview(t.threadId, t.userQuery)}</span>
				<span class="chat-meta">
					{#if $namespace === '' && t.namespace}<span class="crew-badge">{crewLabel($crewDirectory, t.namespace, t.crew)}</span>{/if}
					<span>{formatTimestamp(t.startedAt)}</span>
				</span>
			</button>
		{/each}
	</div>

	<div class="main-panel">
		<div class="main-header">
			<h1>Messages</h1>
			<div class="header-controls">
				<label class="details-toggle" title="Show how long each answer took and the tokens it used">
					<input type="checkbox" checked={$showChatDetails} onchange={() => showChatDetails.toggle()} />
					Details
				</label>
				{#if selected}
					<a class="action-btn" href="{resolve('/discussions')}?thread={selected.threadId}" title="Open this thread in Discussions to see the agents' internal discussion">Discussion</a>
					<button class="action-btn" onclick={copyChat} title="Copy this conversation to the clipboard">{copied ? '✓' : '📋'}</button>
					{#if !$readOnly}
						{#if !deleteConfirm}
							<button class="action-btn danger" onclick={() => { deleteConfirm = true; }} title="Delete this conversation (removes its thread and purges its messages)">🗑️</button>
						{:else}
							<span class="delete-prompt">Delete?</span>
							<button class="action-btn-text yes" onclick={deleteChat}>Yes</button>
							<button class="action-btn-text no" onclick={() => { deleteConfirm = false; }}>No</button>
						{/if}
					{/if}
				{/if}
			</div>
		</div>

		<div class="chat-timeline" bind:this={timelineEl}>
			{#if !selected}
				<div class="empty-state">
					<p>{selectedScope ? 'Start a conversation with this crew below.' : 'Select a crew in the top bar to chat with it.'}</p>
				</div>
			{:else}
				{#each turns as turn (turn.id)}
					<div class="bubble {turn.from}">
						<div class="bubble-head">
							<span class="who">{turn.from === 'you' ? 'You' : crewLabel($crewDirectory, selected.namespace, selected.crew) || 'Crew'}</span>
							<span class="when">{formatTimestamp(turn.timestamp)}</span>
						</div>
						{#if turn.from === 'crew'}
							<div class="markdown">{@html marked.parse(turn.text)}</div>
							{#if $showChatDetails && detailsLine(turn.details)}
								<div class="answer-details">{detailsLine(turn.details)}</div>
							{/if}
						{:else}
							<div class="plain">{turn.text}</div>
						{/if}
					</div>
				{/each}
				{#if selected.status === 'open'}
					<div class="waiting">The crew is working on an answer...</div>
				{/if}
			{/if}
		</div>

		{#if !$readOnly}
			<div class="compose-bar">
				<textarea
					rows="2"
					bind:value={draft}
					placeholder={selectedScope ? 'Message the crew...' : 'Select a crew to send a message'}
					disabled={!selectedScope}
					onkeydown={(e) => {
						if (e.key === 'Enter' && !e.shiftKey) {
							e.preventDefault();
							send();
						}
					}}
				></textarea>
				<button onclick={send} disabled={!sendable} title={selectedScope ? 'Send to the crew' : 'Select a crew first'}>Send</button>
			</div>
		{/if}
	</div>
</div>

<style>
	.messages-page { display: flex; height: 100%; overflow: hidden; }
	.sidebar-panel {
		width: 260px; border-right: 1px solid var(--color-border); display: flex;
		flex-direction: column; padding: 1rem; flex-shrink: 0; overflow-y: auto; gap: 0.4rem;
	}
	.panel-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
	.panel-header h2 { font-size: 0.9rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--color-text-muted); }
	.new-btn, .compose-bar button {
		background: var(--color-primary); color: #fff; border: none; border-radius: 0.3rem;
		padding: 0.3rem 0.75rem; cursor: pointer; font-size: 0.8rem;
	}
	.compose-bar button:disabled { opacity: 0.5; cursor: not-allowed; }
	.hint { color: var(--color-text-muted); font-size: 0.85rem; }
	.chat-row {
		display: flex; flex-direction: column; gap: 0.2rem; text-align: left; background: none;
		border: 1px solid transparent; border-radius: 0.4rem; padding: 0.5rem; cursor: pointer; color: var(--color-text);
	}
	.chat-row:hover { background: var(--color-bg-tertiary); }
	.chat-row.active { background: var(--color-bg-tertiary); border-color: var(--color-primary); }
	.chat-preview { font-size: 0.85rem; overflow: hidden; text-overflow: ellipsis; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; }
	.chat-meta { display: flex; gap: 0.5rem; font-size: 0.7rem; color: var(--color-text-muted); }
	.crew-badge { font-family: var(--font-mono); }

	.main-panel { flex: 1; display: flex; flex-direction: column; min-width: 0; }
	.main-header {
		display: flex; justify-content: space-between; align-items: center; gap: 1rem;
		padding: 0.75rem 1rem; border-bottom: 1px solid var(--color-border);
	}
	.main-header h1 { font-size: 1.25rem; font-weight: 600; }
	.header-controls { display: flex; align-items: center; gap: 0.5rem; }
	.details-toggle { display: flex; align-items: center; gap: 0.3rem; font-size: 0.8rem; color: var(--color-text-muted); cursor: pointer; }
	.action-btn, .action-btn-text {
		background: none; border: 1px solid var(--color-border); border-radius: 0.3rem;
		padding: 0.15rem 0.5rem; cursor: pointer; color: var(--color-text-muted); font-size: 0.8rem; text-decoration: none;
	}
	.action-btn:hover { border-color: var(--color-primary); text-decoration: none; }
	.action-btn.danger:hover { border-color: var(--color-error); }
	.action-btn-text.yes { color: var(--color-error); border-color: var(--color-error); }
	.delete-prompt { font-size: 0.8rem; color: var(--color-error); }

	.chat-timeline { flex: 1; overflow-y: auto; padding: 1rem; display: flex; flex-direction: column; gap: 0.75rem; }
	.empty-state { margin: auto; color: var(--color-text-muted); }
	.bubble { max-width: 75%; border-radius: 0.6rem; padding: 0.6rem 0.9rem; border: 1px solid var(--color-border); }
	.bubble.you { align-self: flex-end; background: var(--color-bg-tertiary); }
	.bubble.crew { align-self: flex-start; background: var(--color-bg-secondary); }
	.bubble-head { display: flex; gap: 0.75rem; font-size: 0.7rem; color: var(--color-text-muted); margin-bottom: 0.3rem; }
	.who { font-weight: 600; }
	.plain { white-space: pre-wrap; overflow-wrap: anywhere; }
	.answer-details { margin-top: 0.4rem; font-size: 0.72rem; color: var(--color-text-muted); font-family: var(--font-mono); }
	.waiting { color: var(--color-text-muted); font-size: 0.85rem; align-self: flex-start; }

	.compose-bar { display: flex; gap: 0.5rem; padding: 0.75rem 1rem; border-top: 1px solid var(--color-border); align-items: flex-end; }
	.compose-bar textarea {
		flex: 1; resize: none; background: var(--color-bg-secondary); color: var(--color-text);
		border: 1px solid var(--color-border); border-radius: 0.4rem; padding: 0.5rem; font: inherit;
	}
</style>
