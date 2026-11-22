<script lang="ts">
	import ConversationDeleteDialog from './ConversationDeleteDialog.svelte';
	import { deleteTarget, type RowThread } from '#lib/row-actions.js';
	import type { ErasureReport } from '#lib/conversation-actions-client.js';

	interface Props {
		thread: RowThread;
		/** The first question, to say which conversation goes. */
		title: string;
		onclose: () => void;
		ondeleted: (report: ErasureReport) => void;
	}

	// The delete of the conversation a list row stands for; nothing shows for a thread that cannot be deleted.
	let { thread, title, onclose, ondeleted }: Props = $props();
	const target = $derived(deleteTarget(thread));
</script>

{#if target}
	<ConversationDeleteDialog scope={target.scope} conversationId={target.id} {title} {onclose} {ondeleted} />
{/if}
