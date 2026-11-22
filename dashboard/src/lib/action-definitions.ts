// The tooltip text of every action in the dashboard (buttons, menu items, toggles,
// tabs, links that act, tiles that open a page). Each says in a few words what the
// action does and, where it matters, what it affects. Use it with the shared tooltip:
//
//   <button use:tooltip={ACTIONS.copyYaml}>Copy</button>
//
// Add the text here, never inline in a page. A test fails when an action has no tooltip.

function debugTip(on: boolean, hidden: number): string {
	if (on) return 'Hide debug detail: back to the answer and the agents\' messages';
	const more = hidden > 0 ? hidden + ' more messages, ' : '';
	return 'Show debug detail: ' + more + 'timings, tokens and placement';
}

export const ACTIONS = {
	// Shared controls
	refresh: 'Refresh: reloads the data on this page now; changes nothing in the cluster',
	help: 'Help: explains this column or term in a short note',
	systemInfo: 'System info: Kubemoot versions and how long each component has been up',
	copySystemInfo: 'Copy: the versions and uptime as text, for a support request',
	copyYaml: 'Copy this resource\'s YAML, exactly as shown',
	toggleManagedFields: 'Show or hide metadata.managedFields, the record of which tool set each field',
	resourceTab: (tab: 'overview' | 'yaml') =>
		tab === 'yaml'
			? 'YAML: this resource exactly as the cluster returns it; read-only'
			: 'Overview: this resource\'s state and settings in readable form',
	copyCommand: 'Copy: the command to the clipboard; run it in your terminal',

	chooseCrew: 'Crew: limits every list on this page to one crew, or shows all crews',
	downloadConversations: (count: number) =>
		`Download: everything Kubemoot holds about ${count === 1 ? 'this conversation' : 'these conversations'}, as a zip built by the operator`,

	// Discussion (Moot) page and its Actions menu
	discussionActions: 'Actions: copy or download this discussion, or copy a command that does it from your terminal',
	showTimeline: 'Show timeline: lists every agent message in time order under the answer',
	showDebug: debugTip,
	copyDiscussion: 'Copy discussion: the question, answer and every agent\'s message, as Markdown',
	copyThreadId: 'Copy thread id: the id together with the question, to paste into a report or a command',
	downloadConversation: 'Download conversation: a zip of the questions, answers and messages',
	copyKmctl: (label: string) => `${label}: copies this command; run it in your terminal`,
	howAnswerMade: 'How this answer was made: which agents took part and what each contributed',
	showAlternatives: 'Alternatives: other places this model could have run, and why they were not chosen',
	openThread: 'Open: reads how the agents settled this question; changes nothing',
	loadOlder: 'Load older threads: fetches earlier discussions; changes nothing',
	toggleSynthesis: 'More or less: expands or collapses the full synthesis text',
	downloadArtifact: 'Download: the full text that was too long to show inline',
	copyGraphImage: 'Copy image: the agent graph as a picture to the clipboard',

	// Conversations page
	selectConversation: 'Select: adds this conversation to the download of selected conversations',
	openConversation: 'Open: shows this conversation\'s questions and answers',
	toggleDetails: 'Details: shows how long each answer took and the tokens it used',
	copyConversation: 'Copy conversation: its questions and answers to the clipboard as text',

	// Conversations page: asking, following up, acting on a conversation, filtering
	newConversation: '+ New: starts a conversation with a crew; nothing is sent until you ask a question',
	sendQuestion: (replying: boolean) =>
		replying
			? 'Reply: sends your follow-up in this conversation as you; the crew answers in it'
			: 'Ask: sends your question to the crew as you and starts a new conversation',
	chooseCrewToAsk: 'Crew to ask: where this new conversation goes; the top-bar Crew selection is not changed',
	conversationActions: 'Actions: copy, download, stop, pause, resume or delete this conversation',
	openListItem: (kind: 'thread' | 'conversation') =>
		kind === 'thread'
			? 'Open: reads how the agents settled this question; changes nothing'
			: 'Open: shows this conversation\'s questions and answers',
	menuItem: (text: string) => text,
	showAnswerDetails: 'Show answer details: how long each answer took and the tokens it used; display only',
	copyConversationId: 'Copy conversation id: the id with the first question, to paste into a report or a command',
	openInMoot: 'Open in the Moot: reads how the agents settled this question; changes nothing',
	pauseConversation: 'Pause: holds the crew\'s work on this conversation until you resume it',
	resumeConversation: 'Resume: lets the crew carry on with the paused conversation',
	stopConversation: 'Stop: ends the crew\'s work on this conversation now; it cannot be resumed',
	deleteConversation: 'Delete: asks first, then erases this conversation from every store that holds it',
	confirmDelete: 'Yes, delete: erases the conversation from every store now; this cannot be undone',
	cancelDelete: 'No: keeps the conversation; nothing is changed',
	stopAndDelete: 'Stop and delete: stops the running discussion, then erases the conversation from every store',
	repeatDelete: 'Repeat delete: tries again to clear the stores that failed',
	continueWithReason: 'Continue: does the action on this conversation and records your reason in the gateway audit log',
	cancelAction: 'Cancel: closes this question; nothing is changed',
	dismissNotice: 'Dismiss: closes this message; nothing is changed',
	closeReport: 'Close: hides this report; the deletion stays done',
	searchConversations: 'Search: words must all match, -word leaves out matches, "a phrase" matches as written; display only',
	filterStatus: 'Status: shows only active, closed or failed conversations; display only',
	filterMine: 'Mine: shows only the conversations you started; display only',
	filterDates: 'Dates: opens the From and To days; shows only conversations started in that range; display only',
	filterFrom: 'From: shows only conversations started on or after this day; display only',
	filterTo: 'To: shows only conversations started on or before this day; display only',
	removeFilter: 'Remove: stops filtering by this; nothing else changes',
	loadOlderMatches: 'Load older: reads earlier discussions from the bus so the search can reach them; changes nothing',

	// Signing in and out
	signIn: 'Sign in: checks your token with the cluster, then the dashboard acts as you',
	howToGetToken: 'How to get a token: shows the kmctl and kubectl commands that mint one',
	signOut: 'Sign out: forgets your token on this dashboard; nothing in the cluster changes',

	// Other pages
	exportMemory: 'Export: downloads this crew\'s memory as an OMPI zip, one JSON file per fact with who learned it and when. Credential facts are left out.',
	exportMemoryEmpty: 'Nothing to export: this crew has not learned any facts yet',
	toggleStandAsides: 'Stand-asides: shows or hides agents that stood aside in a discussion; display only',
	filterVerdict: 'Verdict: shows only servers with this reliability verdict',
	sortReports: 'Sort: orders the reports; changes nothing in the cluster',
	openModelCRs: (name: string) => `Open ${name}: the Model resources that use this model`,
	resetLayout: 'Reset layout: returns the agents to the automatic arrangement; dragged positions are forgotten',
	clearModelFilter: 'Clear: removes the filter and shows every model',
	toggleAssembledPrompt: 'Show or hide the full prompt this agent receives, assembled from its prompt modules',
	toggleReportRow: 'Expand: shows or hides this server\'s test trials',
	openGithub: 'Open: this server\'s GitHub page in a new tab',
	copySuite: 'Copy: this suite\'s details to the clipboard as text',
	downloadSuiteReport: (size: string) => `Download: the suite's XLSX report (${size})`,
	toggleSection: (collapsible: boolean) => (collapsible ? 'Show or hide this section' : ''),
	expandSuite: 'Expand: shows or hides this suite\'s scenarios and runs',
	expandScenario: 'Expand: shows or hides this scenario\'s iterations',
	expandIteration: 'Expand: shows or hides this iteration\'s threads and evidence',
	toggleFinding: 'More or less: expands or collapses this finding',
	openAgent: (name: string) => `Open ${name}: this agent's page`,
	openTile: (label: string) => `Open ${label}: the list of these resources and their state`,
	openNats: 'Open NATS: server, streams and stored data; read-only'
} as const;

export type ActionKey = keyof typeof ACTIONS;
