// Validation for the Memory page's add-or-update form. The crew and namespace come
// from the top-bar crew picker (the shared selection), never from a text field.

import type { CrewScope } from '#lib/crewScope.js';

export interface FactDraft {
	topic: string;
	key: string;
	value: string;
}

/** What blocks saving the draft, in words for the person; null when it can be saved. */
export function factFormProblem(scope: CrewScope | null, draft: FactDraft): string | null {
	if (!scope) return 'Pick one crew in the top bar: a fact belongs to a single crew.';
	if (!draft.topic.trim()) return 'Enter a topic, the group the fact belongs to.';
	if (!draft.key.trim()) return 'Enter a key, the name of the fact within its topic.';
	if (!draft.value.trim()) return 'Enter the value to remember.';
	return null;
}

/** Whether Save is enabled. */
export function canSaveFact(scope: CrewScope | null, draft: FactDraft): boolean {
	return factFormProblem(scope, draft) === null;
}

/** The draft with surrounding whitespace removed, as it is sent. */
export function trimmedDraft(draft: FactDraft): FactDraft {
	return { topic: draft.topic.trim(), key: draft.key.trim(), value: draft.value.trim() };
}
