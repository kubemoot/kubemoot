// The tooltip text of the dashboard's icon-only buttons and of a terse small control such as
// the Mine checkbox. A tooltip goes only on a button or a clickable icon whose name alone does
// not say enough; a text button, a menu item, a tile, a list row, a field or a title carries
// none. An (i) help icon sits beside a page title or a larger section or concept, never beside
// a small control. Use it with the shared tooltip:
//
//   <button use:tooltip={ACTIONS.refresh} aria-label="Refresh">...</button>
//
// Add the text here, never inline in a page. A test fails when an icon-only button has no
// tooltip or aria-label, and when anything else has a tooltip.

/** The tooltip of each icon in the hover bars; ACTIONS.hoverTip reads them by the action's id. */
const HOVER_TIPS = {
	'copy-message': 'Copy: this message\'s text',
	'edit-question': 'Edit: puts this question in the ask field to send as a follow-up',
	'copy-id': 'Copy id: the id with the question, to paste into a report or a command',
	'delete-conversation': 'Delete: asks first, then erases this conversation from every store that holds it'
} as const;

export type HoverId = keyof typeof HOVER_TIPS;

export const ACTIONS = {
	hoverTip: (id: HoverId) => HOVER_TIPS[id] ?? 'Run this action',
	rowActions: 'Actions: more things to do with this row',
	viewOptions: 'View options: copy the shown text, or show the managed fields',
	filterMine: 'Only conversations you started',
	listToggle: (collapsed: boolean) => (collapsed ? 'Show the list' : 'Hide the list: gives the open item the full width'),
	refresh: 'Refresh: reloads the data on this page now; changes nothing in the cluster',
	systemInfo: 'System info: Kubemoot versions and how long each component has been up',
	copySystemInfo: 'Copy: the versions and uptime as text, for a support request',
	conversationActions: 'Actions: copy, download, stop, pause, resume or delete this conversation',
	discussionActions: 'Actions: copy or download this discussion',
	copyConversation: 'Copy conversation: its questions and answers to the clipboard as text',
	copyThreadId: 'Copy thread id: the id together with the question, to paste into a report',
	copyGraphImage: 'Copy image: the agent graph as a picture to the clipboard',
	sendQuestion: (replying: boolean) =>
		replying
			? 'Reply: sends your follow-up in this conversation as you; the crew answers in it'
			: 'Ask: sends your question to the crew as you and starts a new conversation',
	dismissNotice: 'Dismiss: closes this message; nothing is changed',
	exportMemory: 'Export: downloads this crew\'s memory as an OMPI zip, one JSON file per fact with who learned it and when. Credential facts are left out.',
	exportMemoryEmpty: 'Nothing to export: this crew has not learned any facts yet'
} as const;

export type ActionKey = keyof typeof ACTIONS;
