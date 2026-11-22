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
	copyYaml: 'Copy: this resource\'s YAML to the clipboard',
	copyCommand: 'Copy: the command to the clipboard; run it in your terminal',
	expandYaml: 'YAML: shows this resource exactly as the cluster stores it; read-only',

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

	// Other pages
	downloadMemory: 'Download: this crew\'s memory as a file in the OMPI interchange format',
	exportMemory: 'Export: opens the export options; the memory is downloaded only when you choose Download. Format: OMPI interchange',
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
