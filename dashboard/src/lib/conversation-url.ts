// The open conversation lives in the page address, so a link shares it and a reload keeps it:
// ?conversation=<id>. A thread from before conversations carried an id has none, and is opened
// by ?thread=<id> instead. Filters and the search in the same address are left as they are.

export interface OpenRef {
	/** The conversation id, when the open item has one. */
	conversation?: string;
	/** The thread id, for an item with no conversation id. */
	thread?: string;
}

import { isConversationId } from '#lib/conversation-export.js';

/** What the address says is open; anything that is not an id is ignored. */
export function openRef(params: URLSearchParams): OpenRef {
	const conversation = params.get('conversation') ?? '';
	const thread = params.get('thread') ?? '';
	if (isConversationId(conversation)) return { conversation };
	if (isConversationId(thread)) return { thread };
	return {};
}

/** The address query with the open item written into a copy, or removed when nothing is open. */
export function withOpen(params: URLSearchParams, item: { conversationId?: string; threadId: string } | null): URLSearchParams {
	const next = new URLSearchParams(params);
	next.delete('conversation');
	next.delete('thread');
	if (!item) return next;
	if (item.conversationId) next.set('conversation', item.conversationId);
	else next.set('thread', item.threadId);
	return next;
}
