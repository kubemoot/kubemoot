<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { marked } from 'marked';
	import {
		crewDirectory,
		threads,
		sortedThreads,
		initDiscussions,
		showChatDetails,
		namespace
	} from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry, crewLabel } from '#lib/crew-display-name.js';
	import { chatAsText, chatTurns, crewThreads, detailsLine, plainPreview } from '#lib/chat-view.js';
	import { formatTimestamp } from '#lib/discussion-timing.js';
	import { CommandHint, ConversationDownload } from '#lib/components/common/index.js';
	import { askCommand, listConversationsCommand, replayConversationCommand } from '#lib/cli-commands.js';
	import { conversationIds, threadConversationId } from '#lib/conversation-export.js';

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

	let copied = $state(false);

	// The conversation of the open thread, and the ones ticked in the list. A download is for
	// one crew, so ticking is offered when a crew is selected.
	const selectedScopeOfThread = $derived(selected ? tryCrewScope(selected.namespace, selected.crew) : null);
	const selectedConversationId = $derived(selected ? threadConversationId(selected) : undefined);
	let ticked = $state<Record<string, boolean>>({});
	const tickedIds = $derived(conversationIds(chats.filter((t) => ticked[t.threadId])));
	const exportEndpoint = resolve('/api/kubemoot/conversation-export');

	function tick(threadId: string, on: boolean) {
		ticked = { ...ticked, [threadId]: on };
	}

	// Opening a conversation sets the top-bar Crew selector to its crew, so the page, the
	// downloads and the commands all speak for that crew.
	function selectChat(threadId: string | null) {
		selectedThreadId = threadId;
		const ns = threadId ? $threads.get(threadId)?.namespace : undefined;
		if (ns && ns !== $namespace) namespace.set(ns);
	}


	async function copyChat() {
		await navigator.clipboard.writeText(chatAsText(turns, $showChatDetails));
		copied = true;
		setTimeout(() => {
			copied = false;
		}, 2000);
	}

	function preview(threadId: string, fallback: string): string {
		const first = $threads.get(threadId);
		return plainPreview(chatTurns(first?.messages ?? [])[0]?.text || fallback);
	}

	onMount(() => {
		initDiscussions();
	});
</script>

<div class="conversations-page">
	<div class="sidebar-panel">
		<div class="panel-header">
			<h2>Recent</h2>
			{#if selectedScope && tickedIds.length > 0}
				<ConversationDownload scope={selectedScope} ids={tickedIds} endpoint={exportEndpoint} />
			{/if}
		</div>
		{#if chats.length === 0}
			<p class="hint">No conversations{selectedScope ? ' with this crew' : ''} yet.</p>
		{/if}
		{#each chats as t (t.threadId)}
			<div class="chat-item">
			{#if selectedScope && threadConversationId(t)}
				<input type="checkbox" class="chat-tick" aria-label="Select this conversation for download" checked={!!ticked[t.threadId]} onchange={(e) => tick(t.threadId, e.currentTarget.checked)} />
			{/if}
			<button class="chat-row" class:active={t.threadId === selectedThreadId} onclick={() => selectChat(t.threadId)}>
				<span class="chat-preview">{preview(t.threadId, t.userQuery)}</span>
				<span class="chat-meta">
					{#if $namespace === '' && t.namespace}<span class="crew-badge">{crewLabel($crewDirectory, t.namespace, t.crew ?? '')}</span>{/if}
					<span>{formatTimestamp(t.startedAt)}</span>
				</span>
			</button>
			</div>
		{/each}
	</div>

	<div class="main-panel">
		<div class="main-header">
			<h1>Conversations</h1>
			<div class="header-controls">
				<label class="details-toggle" title="Show how long each answer took and the tokens it used">
					<input type="checkbox" checked={$showChatDetails} onchange={() => showChatDetails.toggle()} />
					Details
				</label>
				{#if selected}
					<a class="action-btn" href="{resolve('/discussions')}?thread={selected.threadId}" title="Open this thread in Discussions to see the agents' internal discussion">Discussion</a>
					{#if selectedConversationId}
						<ConversationDownload scope={selectedScopeOfThread} ids={[selectedConversationId]} endpoint={exportEndpoint} label="Download" />
					{/if}
					<button class="action-btn" onclick={copyChat} title="Copy this conversation to the clipboard">{copied ? '✓' : '📋'}</button>
				{/if}
			</div>
		</div>

		<div class="chat-timeline">
			{#if !selected}
				<div class="empty-state">
					<p>{selectedScope || chats.length > 0 ? 'Select a conversation, or start one from a terminal.' : 'Select a crew in the top bar to see its conversations.'}</p>
				</div>
			{:else}
				{#each turns as turn (turn.id)}
					<div class="bubble {turn.from}" id="message-{turn.id}" tabindex="-1">
						<div class="bubble-head">
							<span class="who">{turn.from === 'you' ? 'You' : crewLabel($crewDirectory, selected.namespace, selected.crew ?? '') || 'Crew'}</span>
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

		<div class="compose-bar">
			{#if selected}
				<CommandHint
					label="Reply from a terminal (find the id with {listConversationsCommand(selected.crew ?? 'CREW', selected.namespace)}):"
					command={askCommand(selected.crew ?? 'CREW', selected.namespace, 'CONVERSATION_ID')}
				/>
				{#if selectedConversationId}
					<CommandHint label="Ask this conversation's first question again, in a new conversation:" command={replayConversationCommand(selected.crew ?? 'CREW', selectedConversationId, selected.namespace)} />
				{/if}
			{:else}
				<CommandHint label="Ask a crew from a terminal:" command={askCommand(selectedScope?.crew ?? 'CREW', selectedScope?.namespace ?? 'NAMESPACE')} />
			{/if}
		</div>
	</div>
</div>

<style>
	.conversations-page { display: flex; height: 100%; overflow: hidden; }
	.sidebar-panel {
		width: 260px; border-right: 1px solid var(--color-border); display: flex;
		flex-direction: column; padding: 1rem; flex-shrink: 0; overflow-y: auto; gap: 0.4rem;
	}
	.panel-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
	.panel-header h2 { font-size: 0.9rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--color-text-muted); }
	.hint { color: var(--color-text-muted); font-size: 0.85rem; }
	.chat-row {
		display: flex; flex-direction: column; gap: 0.2rem; text-align: left; background: none;
		border: 1px solid transparent; border-radius: 0.4rem; padding: 0.5rem; cursor: pointer; color: var(--color-text);
	}
	.chat-row:hover { background: var(--color-bg-tertiary); }
	.chat-row.active { background: var(--color-bg-tertiary); border-color: var(--color-primary); }
	.chat-preview { font-size: 0.85rem; overflow: hidden; text-overflow: ellipsis; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; }
	.chat-meta { display: flex; flex-wrap: wrap; gap: 0 0.5rem; white-space: nowrap; font-size: 0.7rem; color: var(--color-text-muted); }
	.crew-badge { font-family: var(--font-mono); }

	.main-panel { flex: 1; display: flex; flex-direction: column; min-width: 0; }
	.main-header {
		display: flex; justify-content: space-between; align-items: center; gap: 1rem;
		padding: 0.75rem 1rem; border-bottom: 1px solid var(--color-border);
	}
	.main-header h1 { font-size: 1.25rem; font-weight: 600; }
	.header-controls { display: flex; align-items: center; gap: 0.5rem; }
	.details-toggle { display: flex; align-items: center; gap: 0.3rem; font-size: 0.8rem; color: var(--color-text-muted); cursor: pointer; }
	.action-btn {
		background: none; border: 1px solid var(--color-border); border-radius: 0.3rem;
		padding: 0.15rem 0.5rem; cursor: pointer; color: var(--color-text-muted); font-size: 0.8rem; text-decoration: none;
	}
	.action-btn:hover { border-color: var(--color-primary); text-decoration: none; }

	.chat-item { display: flex; align-items: center; }
	.chat-item .chat-row { flex: 1; min-width: 0; }
	.chat-tick { margin: 0 0.25rem 0 0.5rem; }

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

	.compose-bar { padding: 0.75rem 1rem; border-top: 1px solid var(--color-border); }
</style>
