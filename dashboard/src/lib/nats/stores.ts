// The shared definition of every NATS store Kubemoot creates (nats-stores.json), typed.
import definition from './nats-stores.json';

export type StoreKind = 'stream' | 'kv' | 'object';
export type PeoplesData = 'yes' | 'derived' | 'maybe' | 'no';
export type StoreOwner = 'crew' | 'system-learning' | 'kubemoot';

export interface StoreDefinition {
	name: string;
	kind: StoreKind;
	holds: string;
	peoplesData: PeoplesData;
	owner: StoreOwner;
	/** Where a crew's share lives; {ns} and {crew} are the crew's namespace and name. */
	crewKey: string | null;
	/** The Crew field that shortens how long the crew's data stays, when there is one. */
	crewPolicy: string | null;
	/** Key pattern of one namespace's share; set on the stores a namespace-scoped dashboard serves. */
	namespaceKey?: string;
	/** The bootstrap job's size limit in bytes; null when a chart value sets it. */
	defaultMaxBytes: number | null;
	/** Who creates the store: absent for the operator chart's bootstrap job, or the crew liaison. */
	createdBy?: 'liaison';
}

export const STORES: readonly StoreDefinition[] = definition.stores as StoreDefinition[];

const BY_NAME = new Map(STORES.map((s) => [s.name, s]));

/** The JetStream stream that holds a store's data: KV buckets and object stores sit in prefixed streams. */
export function jetstreamName(store: Pick<StoreDefinition, 'name' | 'kind'>): string {
	if (store.kind === 'kv') return `KV_${store.name}`;
	if (store.kind === 'object') return `OBJ_${store.name}`;
	return store.name;
}

/** The definition behind a JetStream stream name (KV_ and OBJ_ prefixes included), or undefined. */
export function storeForStream(streamName: string): StoreDefinition | undefined {
	const direct = BY_NAME.get(streamName);
	if (direct) return direct;
	if (streamName.startsWith('KV_')) return pick(BY_NAME.get(streamName.slice(3)), 'kv');
	if (streamName.startsWith('OBJ_')) return pick(BY_NAME.get(streamName.slice(4)), 'object');
	return undefined;
}

function pick(store: StoreDefinition | undefined, kind: StoreKind): StoreDefinition | undefined {
	return store?.kind === kind ? store : undefined;
}

/** Fills {ns} and {crew} in a crewKey template. */
export function crewKeyFor(template: string, namespace: string, crew: string): string {
	return template.replace('{ns}', namespace).replace('{crew}', crew);
}

/** Fills {ns} in a namespaceKey template. */
export function namespaceKeyFor(template: string, namespace: string): string {
	return template.replace('{ns}', namespace);
}

/** The stores that hold data keyed by crew. */
export function crewKeyedStores(): StoreDefinition[] {
	return STORES.filter((s) => s.crewKey !== null);
}

export const OWNER_LABELS: Record<StoreOwner, string> = {
	crew: 'A crew',
	'system-learning': 'System learning',
	kubemoot: 'Kubemoot'
};

export const PEOPLES_DATA_LABELS: Record<PeoplesData, string> = {
	yes: 'Yes',
	derived: 'Yes, derived',
	maybe: 'Only if real questions are replayed',
	no: 'No'
};
