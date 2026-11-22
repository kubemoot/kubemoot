// The icon bar of a conversation row, the same in Conversations and the Moot: Copy id, and, when the
// person may delete it, Delete.

import { copyText } from '#lib/clipboard.js';
import { tryCrewScope } from '#lib/crewScope.js';
import { idWithQuestion } from '#lib/conversation-actions.js';
import { threadConversationId } from '#lib/conversation-export.js';
import type { HoverAction } from '#lib/hover-actions.js';
import type { Thread } from '#lib/stores/discussions.js';

/** The parts of a thread a row needs; a conversation (its threads merged) has them too. */
export type RowThread = Pick<Thread, 'threadId' | 'namespace' | 'crew' | 'messages'>;

/** The crew and conversation a delete would go to, or null when the row cannot be deleted (an older thread, an unknown crew). */
export function deleteTarget(thread: RowThread) {
	const id = threadConversationId(thread);
	const scope = tryCrewScope(thread.namespace, thread.crew ?? '');
	return id && scope ? { id, scope } : null;
}

export function rowActions(thread: RowThread, title: string, options: { canDelete: boolean; ondelete: () => void }): HoverAction[] {
	const id = threadConversationId(thread) ?? thread.threadId;
	const actions: HoverAction[] = [{ label: 'Copy id', id: 'copy-id', icon: 'id', run: () => copyText(idWithQuestion(id, title)) }];
	if (options.canDelete && deleteTarget(thread)) {
		actions.push({ label: 'Delete conversation', id: 'delete-conversation', icon: 'delete', danger: true, run: options.ondelete });
	}
	return actions;
}
