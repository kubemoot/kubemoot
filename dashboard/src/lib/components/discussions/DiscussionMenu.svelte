<script lang="ts">
	import { ACTIONS } from '#lib/action-definitions.js';
	import ActionMenu, { type MenuEntry } from '#lib/components/common/ActionMenu.svelte';

	interface Props {
		/** The span graph ("timeline") is shown. */
		showTimeline: boolean;
		showDebug: boolean;
		ontoggletimeline: () => void;
		ontoggledebug: () => void;
		oncopydiscussion: () => void;
		oncopythreadid: () => void;
		/** The export link for this conversation; absent when it cannot be downloaded. */
		downloadHref?: string | null;
		/** The Conversations page opened on this thread's conversation; absent for a thread with no conversation. */
		conversationHref?: string | null;
	}

	let {
		showTimeline, showDebug, ontoggletimeline, ontoggledebug, oncopydiscussion, oncopythreadid,
		downloadHref = null, conversationHref = null
	}: Props = $props();

	const entries = $derived<MenuEntry[]>([
		{ kind: 'check', label: 'Show timeline', checked: showTimeline, onselect: ontoggletimeline },
		{ kind: 'check', label: 'Debug detail', checked: showDebug, onselect: ontoggledebug },
		{ kind: 'separator' },
		{ kind: 'item', label: 'Copy discussion', onselect: oncopydiscussion },
		{ kind: 'item', label: 'Copy thread id with the question', onselect: oncopythreadid },
		...(conversationHref ? [{ kind: 'link', label: 'View associated conversation', href: conversationHref } satisfies MenuEntry] : []),
		...(downloadHref ? [{ kind: 'link', label: 'Download conversation', href: downloadHref, download: true } satisfies MenuEntry] : [])
	]);
</script>

<ActionMenu {entries} tip={ACTIONS.discussionActions} />
