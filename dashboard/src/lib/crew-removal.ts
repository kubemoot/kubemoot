// Removing a crew from the Crews page: the words of the in-page confirmation and
// the request. The dashboard deletes the Crew object only; the operator's finalizer
// and owner references remove its Agents and open discussions.

/** What the confirmation says about the crew being removed. */
export interface CrewRemovalTarget {
	/** Name the page shows for the crew. */
	label: string;
	namespace: string;
	name: string;
	agentCount?: number;
}

/** The confirmation prompt: it names the crew and says what goes with it. */
export function removalPrompt(target: CrewRemovalTarget): string {
	const agents =
		target.agentCount === undefined
			? 'its Agents'
			: `its ${target.agentCount} ${target.agentCount === 1 ? 'Agent' : 'Agents'}`;
	return `Remove crew ${target.label}? ${agents} and its open discussions are removed with it. This cannot be undone.`;
}

/** The tooltip on the remove button: it states the purpose. */
export function removalTooltip(label: string): string {
	return `Remove crew ${label} (deletes the Crew and, with it, its Agents and open discussions)`;
}

/** The request that deletes the Crew object. */
export async function removeCrew(
	fetcher: typeof fetch,
	url: string,
	namespace: string
): Promise<{ ok: true } | { ok: false; error: string }> {
	try {
		const res = await fetcher(`${url}?namespace=${encodeURIComponent(namespace)}`, { method: 'DELETE' });
		if (res.ok) return { ok: true };
		const body = await res.json().catch(() => ({}));
		return { ok: false, error: typeof body?.error === 'string' ? body.error : `HTTP ${res.status}` };
	} catch (e) {
		return { ok: false, error: e instanceof Error ? e.message : 'Failed to remove crew' };
	}
}
