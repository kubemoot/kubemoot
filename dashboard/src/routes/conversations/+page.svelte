<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { marked } from 'marked';
	import { tooltip } from '#lib/tooltip.js';
	import { ACTIONS } from '#lib/action-definitions.js';
	import {
		crewDirectory,
		sortedThreads,
		initDiscussions,
		forgetThreads,
		showChatDetails,
		namespace,
		hasOlder,
		loadOlder
	} from '#lib/stores/index.js';
	import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
	import { crewEntry, crewLabel, crewTooltip, pickerLabel } from '#lib/crew-display-name.js';
	import { chatAsText, chatTurns, crewThreads, detailsLine } from '#lib/chat-view.js';
	import { groupConversations, onBus, withPending, type Conversation, type PendingAsk } from '#lib/conversation-group.js';
	import { threadStartedAt } from '#lib/stores/discussions.js';
	import { openRef, withOpen } from '#lib/conversation-url.js';
	import { mineScopesFor } from '#lib/list-scope.js';
	import { formatTimestamp } from '#lib/discussion-timing.js';
	import { ConversationDownload, CopyIcon } from '#lib/components/common/index.js';
	import { CrewBadge, DetailHeader, ListCard, ListDetailLayout, ThreadStatus } from '#lib/components/layout/index.js';
	import { browserTimeZone, conversationIds, exportHref, threadConversationId } from '#lib/conversation-export.js';
	import { conversationTitle, runState } from '#lib/conversation-actions.js';
	import type { ErasureReport } from '#lib/conversation-actions-client.js';
	import { page } from '$app/state';
	import { AskBox, ConversationActions, ErasureReport as ErasureReportPanel, FilterBar } from '#lib/components/conversations/index.js';
	import type { Live } from '#lib/components/conversations/AskBox.svelte';
	import { listMine } from '#lib/ask-client.js';
	import {
		firstHit,
		highlight,
		emptyBecauseOfFilter,
		matchesFilter,
		snippet
	} from '#lib/conversation-filter.js';
	import { createListFilter } from '#lib/list-filter.svelte.js';

	marked.setOptions({ breaks: true, gfm: true });

	// The crew picker is the top-bar "Crew:" selector, the same one the Moot uses.
	const selectedScope = $derived<CrewScope | null>(
		$namespace === '' ? null : tryCrewScope($namespace, crewEntry($crewDirectory, $namespace)?.crew)
	);
	const signedIn = $derived(!!page.data.user);

	// The filters and the search live in the URL query, so a link reproduces the view; a link with
	// none opens on the filter this viewer last chose.
	const listFilter = createListFilter('conversations', '/conversations');
	onMount(() => listFilter.restore());
	const filter = $derived(listFilter.filter);
	const setFilter = listFilter.set;

	// The conversations the signed-in person started, listed by each crew's gateway with their own
	// token: the selected crew, or every crew when none is selected. A crew the person may not use
	// lists nothing.
	let mine = $state<Set<string> | null>(null);
	let mineError = $state('');

	async function loadMine(scopes: CrewScope[]) {
		mine = null;
		mineError = '';
		if (!signedIn || scopes.length === 0) return;
		const found = await listMine(fetch, scopes);
		mine = found.ids;
		mineError = found.error;
	}

	const mineScopes = $derived(mineScopesFor(selectedScope, $crewDirectory));

	$effect(() => {
		void loadMine(mineScopes);
	});

	// One entry per conversation: a follow-up is a new thread under the same conversation id.
	// The open one is looked up among every conversation, so a link opens it whatever crew the
	// top-bar selector shows; the selector is only ever the person's own choice.
	// A question the crew has just taken is listed at once, from what the page knows, until the bus
	// shows its thread.
	let asks = $state<PendingAsk[]>([]);
	const conversations = $derived(groupConversations(withPending($sortedThreads, asks)));

	// The gateway's own stream feeds the stand-in its answer and state; a stand-in the bus now shows is dropped.
	$effect(() => {
		const current = live;
		const id = askedId;
		const known = untrack(() => asks);
		const kept = known.filter((a) => !onBus(a, $sortedThreads));
		const next = current && id ? kept.map((a) => (a.conversationId === id && (a.answer !== current.answer.answer || a.state !== current.answer.phase) ? { ...a, answer: current.answer.answer, state: current.answer.phase } : a)) : kept;
		if (next.length !== known.length || next.some((a, i) => a !== known[i])) asks = next;
	});
	const chats = $derived(
		crewThreads(conversations, selectedScope)
			.filter((t) => chatTurns(t.messages).length > 0)
			.filter((t) => matchesFilter(t, filter, mine))
	);

	// The open conversation is followed by its id when it has one, so it stays open when the
	// stand-in for a just-asked question gives way to the crew's own thread.
	let selectedThreadId = $state<string | null>(null);
	let selectedConversation = $state<string | null>(null);
	const selected = $derived(
		(selectedThreadId ? conversations.find((c) => c.threadIds.includes(selectedThreadId ?? '')) : undefined) ??
			(selectedConversation ? conversations.find((c) => threadConversationId(c) === selectedConversation) : undefined)
	);

	// The open conversation is in the address (?conversation=<id>): a link shares it and a reload
	// keeps it. The address is read again whenever it changes, once the conversation has loaded.
	const wanted = $derived(openRef(new URLSearchParams(page.url.search)));
	let appliedFromUrl = '';
	$effect(() => {
		const key = wanted.conversation ?? wanted.thread ?? '';
		if (key === '' || key === appliedFromUrl) return;
		const found = conversations.find((c) => (wanted.conversation ? threadConversationId(c) === wanted.conversation : c.threadIds.includes(wanted.thread ?? '')));
		if (!found) return;
		appliedFromUrl = key;
		openConversation(found, true);
	});

	function showInUrl(item: { conversationId?: string; threadId: string } | null) {
		const next = withOpen(new URLSearchParams(page.url.search), item);
		appliedFromUrl = item ? (item.conversationId ?? item.threadId) : '';
		const query = next.toString();
		void goto(resolve('/conversations') + (query ? `?${query}` : ''), { replace: true, reset: false });
	}
	const turns = $derived(selected ? chatTurns(selected.messages) : []);
	const selectedScopeOfThread = $derived(selected ? tryCrewScope(selected.namespace, selected.crew) : null);
	const selectedConversationId = $derived(selected ? threadConversationId(selected) : undefined);

	// A new conversation: the question field at the bottom starts one with the selected crew; the
	// conversation opens as soon as the bus shows it, and its answer arrives live like any other.
	let starting = $state(false);
	let live = $state<Live | null>(null);
	let askedId = $state<string | null>(null);
	let openedFor: string | null = null;

	$effect(() => {
		if (!askedId || openedFor === askedId) return;
		if (selectedConversationId === askedId) {
			openedFor = askedId;
			return;
		}
		const found = conversations.find((c) => threadConversationId(c) === askedId);
		if (!found) return;
		openedFor = askedId;
		openConversation(found);
	});

	// The crew has taken a question: list it at once, open it, and put it in the address.
	function asked(id: string, question: string, requestedAt?: string) {
		const crew = selected ? selectedScopeOfThread : newScope;
		if (crew) {
			const fresh: PendingAsk = { conversationId: id, question, namespace: crew.namespace, crew: crew.crew, askedAt: requestedAt ?? new Date().toISOString(), state: 'working' };
			asks = [...asks.filter((a) => a.conversationId !== id || a.question !== question), fresh];
		}
		askedId = id;
	}

	// The crew a new conversation goes to: the top-bar selection, or one chosen here when the
	// selector shows all crews. Choosing here never changes the selector.
	let chosenNamespace = $state('');
	const newScope = $derived<CrewScope | null>(
		selectedScope ?? (chosenNamespace === '' ? null : tryCrewScope(chosenNamespace, crewEntry($crewDirectory, chosenNamespace)?.crew))
	);

	function startNew() {
		selectedThreadId = null;
		selectedConversation = null;
		askedId = null;
		live = null;
		erasure = null;
		starting = true;
		showInUrl(null);
	}

	// The one way a conversation opens: by a click, by the address, or because the person just asked it.
	function openConversation(item: Conversation, fromUrl = false) {
		selectedThreadId = item.threadId;
		selectedConversation = threadConversationId(item) ?? null;
		starting = false;
		erasure = null;
		if (!fromUrl) showInUrl({ conversationId: threadConversationId(item), threadId: item.threadId });
	}

	function selectChat(item: Conversation) {
		live = null;
		openConversation(item);
	}

	function pickCrew(event: Event) {
		chosenNamespace = (event.currentTarget as HTMLSelectElement).value;
	}

	// A deleted conversation leaves the list at once, and the page says what was removed.
	let erasure = $state<{ report: ErasureReport; question: string } | null>(null);

	function deleted(report: ErasureReport) {
		erasure = { report, question: selected ? conversationTitle(selected) : '' };
		forgetThreads([...(report.threads ?? []), ...(selected?.threadIds ?? [])]);
		asks = asks.filter((a) => a.conversationId !== report.conversationId);
		selectedThreadId = null;
		selectedConversation = null;
		showInUrl(null);
	}

	// The conversations ticked in the list, for a download of several. A download is for one crew,
	// so ticking is offered when a crew is selected.
	let ticked = $state<Record<string, boolean>>({});
	const tickedIds = $derived(conversationIds(chats.filter((t) => ticked[t.threadId])));
	const exportEndpoint = resolve('/api/kubemoot/conversation-export');

	function tick(threadId: string, on: boolean) {
		ticked = { ...ticked, [threadId]: on };
	}

	const downloadHref = $derived(
		selectedConversationId ? exportHref(exportEndpoint, selectedScopeOfThread, [selectedConversationId], browserTimeZone()) : null
	);

	let copiedConversation = $state(false);

	async function copyConversation() {
		await navigator.clipboard.writeText(chatAsText(turns, $showChatDetails));
		copiedConversation = true;
		setTimeout(() => (copiedConversation = false), 2000);
	}

	const noConversations = $derived(
		emptyBecauseOfFilter(filter, 'conversations') ??
			(selectedScope ? 'No conversations with this crew yet.' : 'No conversations yet.')
	);

	const waiting = $derived.by(() => {
		const state = runState(selected);
		if (!state.running) return '';
		return state.paused ? 'Paused. Resume it from the menu to continue.' : 'The crew is working on an answer...';
	});

	onMount(() => {
		initDiscussions();
	});
</script>

<ListDetailLayout route="/conversations" detailOpen={!!selected || starting || !!live || !!erasure}>
	{#snippet list()}
		<FilterBar {filter} {signedIn} loaded={$sortedThreads.length} hasOlder={$hasOlder} onchange={setFilter} onloadolder={() => void loadOlder()} error={mineError}>
			{#snippet trailing()}
				{#if signedIn}
					<button type="button" class="new-btn" onclick={startNew}>+ New</button>
				{/if}
			{/snippet}
		</FilterBar>
		{#if selectedScope && tickedIds.length > 0}
			<div class="download-row"><ConversationDownload scope={selectedScope} ids={tickedIds} endpoint={exportEndpoint} /></div>
		{/if}
		{#if chats.length === 0}
			<p class="hint">{noConversations}</p>
		{/if}
		{#each chats as t (t.threadId)}
			<ListCard selected={selected?.threadId === t.threadId} onopen={() => selectChat(t)}>
				{#snippet leading()}
					{#if selectedScope && threadConversationId(t)}
						<input type="checkbox" aria-label="Select this conversation for download" checked={!!ticked[t.threadId]} onchange={(e) => tick(t.threadId, e.currentTarget.checked)} />
					{:else if selectedScope}
						<span class="tick-space"></span>
					{/if}
				{/snippet}
				{#snippet head()}
					{#if t.namespace && t.crew}
						<CrewBadge label={crewLabel($crewDirectory, t.namespace, t.crew ?? '')} title={crewTooltip($crewDirectory, t.namespace, t.crew ?? '')} />
					{:else}
						<span></span>
					{/if}
					<ThreadStatus status={t.status} />
				{/snippet}
				{#each highlight(filter.q ? snippet(firstHit(t, filter.q) ?? conversationTitle(t), filter.q) : conversationTitle(t), filter.q) as piece, i (i)}{#if piece.hit}<mark>{piece.text}</mark>{:else}{piece.text}{/if}{/each}
				{#snippet foot()}
					<span></span>
					<span title={threadStartedAt(t)}>{formatTimestamp(threadStartedAt(t))}</span>
				{/snippet}
			</ListCard>
		{/each}
	{/snippet}

	{#snippet detail()}
		{#if selected}
			<DetailHeader>
				{#if selected.crew}
					<CrewBadge label={crewLabel($crewDirectory, selected.namespace, selected.crew)} title={crewTooltip($crewDirectory, selected.namespace, selected.crew)} />
				{/if}
				<ThreadStatus status={selected.status} />
				{#snippet actions()}
					<button class="action-btn" use:tooltip={ACTIONS.copyConversation} onclick={copyConversation} aria-label="Copy conversation"><CopyIcon done={copiedConversation} /></button>
					<ConversationActions
						scope={selectedScopeOfThread}
						conversationId={selectedConversationId}
						question={conversationTitle(selected)}
						running={runState(selected)}
						copyText={() => chatAsText(turns, $showChatDetails)}
						showDetails={$showChatDetails}
						ontoggledetails={() => showChatDetails.toggle()}
						{downloadHref}
						discussionHref="{resolve('/moot')}?thread={selected.latestThreadId}"
						ondeleted={deleted}
					/>
				{/snippet}
			</DetailHeader>
		{/if}

		<div class="conversation">
			<div class="chat-timeline">
				{#if erasure}
					<ErasureReportPanel report={erasure.report} question={erasure.question} ondismiss={() => (erasure = null)} />
				{:else if selected}
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
					{#if waiting}
						<div class="waiting">{waiting}</div>
					{/if}
				{:else if live}
					<div class="bubble you"><div class="bubble-head"><span class="who">You</span></div><div class="plain">{live.question}</div></div>
					<div class="bubble crew">
						<div class="bubble-head"><span class="who">{newScope ? crewLabel($crewDirectory, newScope.namespace, newScope.crew) : 'Crew'}</span></div>
						{#if live.answer.answer}
							<div class="markdown">{@html marked.parse(live.answer.answer)}</div>
						{:else if live.answer.phase === 'error'}
							<div class="error" role="alert">{live.answer.error}</div>
						{:else}
							<div class="waiting">The crew is working on an answer{live.answer.agents.length ? ` (${live.answer.agents.join(', ')})` : ''}...</div>
						{/if}
					</div>
				{:else}
					<div class="empty-state">
						{#if starting && !newScope}
							<p>Which crew do you want to ask?</p>
							<select onchange={pickCrew} aria-label="Crew to ask">
								<option value="">Choose a crew</option>
								{#each $crewDirectory as entry (`${entry.namespace}/${entry.crew}`)}<option value={entry.namespace}>{pickerLabel(entry, $crewDirectory)}</option>{/each}
							</select>
						{:else if starting && newScope}
							<p>New conversation with {crewLabel($crewDirectory, newScope.namespace, newScope.crew)}</p>
						{:else}
							<p>{signedIn ? 'Select a conversation, or start one with + New.' : 'Select a conversation.'}</p>
						{/if}
					</div>
				{/if}
			</div>

			{#if signedIn && !erasure}
				<div class="ask-bar">
					<AskBox
						scope={selected ? selectedScopeOfThread : newScope}
						conversationId={selectedConversationId}
						bind:live
						focus={starting}
						onAsked={asked}
					/>
				</div>
			{/if}
		</div>
	{/snippet}
</ListDetailLayout>

<style>
	.hint { color: var(--color-text-muted); font-size: 0.85rem; }
	.new-btn {
		background: none; border: 1px solid var(--color-border); border-radius: 0.3rem; padding: 0.25rem 0.55rem;
		cursor: pointer; color: var(--color-text); font-size: 0.8rem; white-space: nowrap;
	}
	.new-btn:hover { border-color: var(--color-primary); }
	.download-row { display: flex; }
	.tick-space { display: inline-block; width: 13px; flex-shrink: 0; }

	.conversation { flex: 1; min-height: 0; display: flex; flex-direction: column; }
	.chat-timeline { flex: 1; overflow-y: auto; padding: 0.25rem 0; display: flex; flex-direction: column; gap: 0.75rem; }
	.empty-state { margin: auto; color: var(--color-text-muted); display: flex; flex-direction: column; align-items: center; gap: 0.5rem; }
	.empty-state select {
		padding: 0.3rem 0.5rem; border-radius: 0.3rem; border: 1px solid var(--color-border);
		background: var(--color-bg-secondary); color: var(--color-text); max-width: 24rem;
	}
	.bubble { border-radius: 0.6rem; padding: 0.6rem 0.9rem; border: 1px solid var(--color-border); min-width: 0; overflow-wrap: anywhere; }
	.bubble.you { align-self: flex-end; max-width: 75%; background: var(--color-bg-tertiary); }
	.bubble.crew { align-self: flex-start; max-width: 90%; background: var(--color-bg-secondary); }
	.bubble-head { display: flex; gap: 0.75rem; font-size: 0.7rem; color: var(--color-text-muted); margin-bottom: 0.3rem; }
	.who { font-weight: 600; }
	.plain { white-space: pre-wrap; overflow-wrap: anywhere; }
	/* The crew's answer is markdown: lists stay inside the bubble and long lines and code wrap. */
	.markdown :global(p) { margin: 0 0 0.5rem; }
	.markdown :global(> :last-child) { margin-bottom: 0; }
	.markdown :global(ul), .markdown :global(ol) { margin: 0 0 0.5rem; padding-left: 1.4rem; }
	.markdown :global(li) { margin: 0.1rem 0; }
	.markdown :global(pre) { max-width: 100%; overflow-x: auto; padding: 0.5rem; border-radius: 0.3rem; background: var(--color-bg-tertiary); }
	.markdown :global(code) { font-family: var(--font-mono); font-size: 0.85em; }
	.markdown :global(table) { display: block; max-width: 100%; overflow-x: auto; border-collapse: collapse; }
	.markdown :global(th), .markdown :global(td) { border: 1px solid var(--color-border); padding: 0.2rem 0.5rem; }
	.answer-details { margin-top: 0.4rem; font-size: 0.72rem; color: var(--color-text-muted); font-family: var(--font-mono); }
	.waiting { color: var(--color-text-muted); font-size: 0.85rem; align-self: flex-start; }
	.error { color: var(--color-error); font-size: 0.85rem; }

	.ask-bar { padding-top: 0.6rem; border-top: 1px solid var(--color-border); }
	mark { background: var(--color-warning); color: var(--color-on-warning); border-radius: 0.15rem; }
</style>
