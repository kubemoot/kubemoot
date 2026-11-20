// How the dashboard shows a Crew's phase and capacity. A Crew's Ready condition says it is
// reconciled as declared; its CapacityAvailable condition says whether a model in the cluster
// catalog can run its agents now. A reconciled crew with no capacity has the phase
// WaitingForCapacity: it stays Ready, but it cannot answer yet.

import type { Condition } from '#lib/types/kubemoot.js';

/** The Crew phase for a reconciled crew that no model in the catalog can serve now. */
export const WAITING_FOR_CAPACITY = 'WaitingForCapacity';

/** The Crew condition that says whether a model can run the crew's agents now. */
export const CAPACITY_CONDITION = 'CapacityAvailable';

/** The phase as people read it: "Waiting for capacity" for WaitingForCapacity, the phase otherwise. */
export function crewPhaseLabel(phase: string | undefined): string {
	if (!phase) return 'Unknown';
	return phase === WAITING_FOR_CAPACITY ? 'Waiting for capacity' : phase;
}

/** The CapacityAvailable condition of a crew, or undefined while it has not been assessed. */
export function capacityCondition(conditions: Condition[] | undefined): Condition | undefined {
	return conditions?.find((c) => c.type === CAPACITY_CONDITION);
}

/** Whether the crew can answer now: its phase is not WaitingForCapacity and it is ready. */
export function crewCanAnswer(phase: string | undefined, ready: boolean | undefined): boolean {
	return ready === true && phase !== WAITING_FOR_CAPACITY;
}

const CAPACITY_LABELS: Record<Condition['status'], string> = {
	True: 'Available',
	False: 'Not available',
	Unknown: 'Unknown'
};

/** The CapacityAvailable condition as people read it. */
export function capacityLabel(condition: Condition): string {
	return CAPACITY_LABELS[condition.status] ?? 'Unknown';
}
