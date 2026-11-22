// What deleting a crew removes, in the words the confirmation shows. The operator's finalizer does
// the work; the list is its cleanup, so a person sees what goes before they say yes.

import type { Crew } from '#lib/types/kubemoot.js';

const MANAGE_NAMESPACE = 'kubemoot.ai/manage-namespace';

function agentsLine(count: number | undefined): string {
	if (!count) return 'Its agents';
	return `Its ${count} agent${count === 1 ? '' : 's'}`;
}

export function crewGoes(crew: Crew): string[] {
	const namespace = crew.metadata.namespace ?? '';
	return [
		`The crew ${crew.metadata.name} in ${namespace}`,
		agentsLine(crew.status?.agentCount),
		'Its discussion history and working memory',
		'Its role bindings',
		...(crew.metadata.annotations?.[MANAGE_NAMESPACE] === 'true' ? [`Its namespace ${namespace}, which it asked Kubemoot to manage`] : []),
		...(crew.metadata.labels?.['app.kubernetes.io/managed-by'] === 'Helm' ? ['A Helm or GitOps release that manages this crew may create it again'] : [])
	];
}
