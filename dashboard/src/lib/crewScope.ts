// The one place the dashboard builds and parses every NATS subject, KV key, and
// object key that carries a crew or agent name. Kubernetes namespaces are
// Kubemoot's isolation boundary: each of these identifiers carries the namespace
// FIRST, then the crew or agent, so the same crew name can run in many namespaces
// with no crosstalk. The formats match the agent-runtime, operator, and discussion
// gateway exactly:
//
//   kubemoot.discuss.<ns>.<crew>.<channel>.<thread>   discussion messages
//   kubemoot.chat.<ns>.<agent>                         chat events
//   <ns>.<agent>                                       kubemoot_agent_state key
//   <ns>.<crew>.<topic>.<key>                          kubemoot_crew_memory key
//   <ns>/<crew>/<thread>/<agent>/<signal>-<uuid>       kubemoot_discussion_artifacts object
//
// Builders throw on a missing or malformed part rather than emitting an unscoped
// or wildcard-bearing identifier; parsers return null for anything that is not
// exactly the expected shape.

export interface CrewScope {
	namespace: string;
	crew: string;
}

export interface DiscussSubject extends CrewScope {
	channel: string;
	threadId: string;
}

export interface ChatSubject {
	namespace: string;
	agent: string;
}

export interface CrewMemoryKey extends CrewScope {
	topic: string;
	key: string;
}

export interface ArtifactKey extends CrewScope {
	threadId: string;
	agent: string;
	name: string;
}

const DISCUSS_PREFIX = 'kubemoot.discuss';
const CHAT_PREFIX = 'kubemoot.chat';

/** Every discussion message, across all namespaces and crews. */
export const DISCUSS_ALL = `${DISCUSS_PREFIX}.>`;
/** Every chat event, across all namespaces. */
export const CHAT_ALL = `${CHAT_PREFIX}.>`;

// A Kubernetes namespace is a DNS label: lowercase alphanumerics and hyphens,
// starting and ending alphanumeric, at most 63 characters. It never has a dot, so
// it is always exactly one NATS token.
const NAMESPACE_RE = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;
// Any other single token: no dots, no NATS wildcards, no whitespace, no slashes.
const TOKEN_RE = /^[A-Za-z0-9_=-]+$/;
// An object-key path segment: like a token, but dots are allowed (never `..`).
const SEGMENT_RE = /^[A-Za-z0-9._-]+$/;

function isSegment(value: unknown): value is string {
	return typeof value === 'string' && SEGMENT_RE.test(value) && !value.includes('..');
}

export function isNamespace(value: unknown): value is string {
	return typeof value === 'string' && NAMESPACE_RE.test(value);
}

export function isToken(value: unknown): value is string {
	return typeof value === 'string' && TOKEN_RE.test(value);
}

function requireNamespace(value: string | undefined | null): string {
	if (!isNamespace(value)) {
		throw new Error(`invalid or missing namespace: ${JSON.stringify(value ?? null)}`);
	}
	return value;
}

function requireToken(value: string | undefined | null, what: string): string {
	if (!isToken(value)) {
		throw new Error(`invalid or missing ${what}: ${JSON.stringify(value ?? null)}`);
	}
	return value;
}

/** A validated namespace+crew pair. Throws when either part is missing or malformed. */
export function crewScope(namespace: string | undefined | null, crew: string | undefined | null): CrewScope {
	return { namespace: requireNamespace(namespace), crew: requireToken(crew, 'crew') };
}

/** Same as crewScope but returns null instead of throwing. */
export function tryCrewScope(
	namespace: string | undefined | null,
	crew: string | undefined | null
): CrewScope | null {
	return isNamespace(namespace) && isToken(crew) ? { namespace, crew } : null;
}

function checkedScope(scope: CrewScope): CrewScope {
	return crewScope(scope.namespace, scope.crew);
}

// ---------------------------------------------------------------- discussions

/** `kubemoot.discuss.<ns>.<crew>.<channel>.<thread>` */
export function discussSubject(scope: CrewScope, channel: string, threadId: string): string {
	const s = checkedScope(scope);
	return `${DISCUSS_PREFIX}.${s.namespace}.${s.crew}.${requireToken(channel, 'channel')}.${requireToken(threadId, 'thread id')}`;
}

/** Every discussion message of one crew: `kubemoot.discuss.<ns>.<crew>.>` */
export function discussCrewFilter(scope: CrewScope): string {
	const s = checkedScope(scope);
	return `${DISCUSS_PREFIX}.${s.namespace}.${s.crew}.>`;
}

/** Every discussion message in one namespace: `kubemoot.discuss.<ns>.>` */
export function discussNamespaceFilter(namespace: string): string {
	return `${DISCUSS_PREFIX}.${requireNamespace(namespace)}.>`;
}

/**
 * Every message of one thread, for a stream purge. Scoped to the thread's crew
 * when known, otherwise any namespace, crew, and channel.
 */
export function discussThreadFilter(threadId: string, scope?: CrewScope | null): string {
	const thread = requireToken(threadId, 'thread id');
	if (!scope) return `${DISCUSS_PREFIX}.*.*.*.${thread}`;
	const s = checkedScope(scope);
	return `${DISCUSS_PREFIX}.${s.namespace}.${s.crew}.*.${thread}`;
}

/** Parse `kubemoot.discuss.<ns>.<crew>.<channel>.<thread>`, or null when malformed. */
export function parseDiscussSubject(subject: string | undefined | null): DiscussSubject | null {
	if (typeof subject !== 'string') return null;
	const parts = subject.split('.');
	if (parts.length !== 6 || `${parts[0]}.${parts[1]}` !== DISCUSS_PREFIX) return null;
	const [, , namespace, crew, channel, threadId] = parts;
	if (!isNamespace(namespace) || ![crew, channel, threadId].every(isToken)) return null;
	return { namespace, crew, channel, threadId };
}

// ----------------------------------------------------------------------- chat

/** `kubemoot.chat.<ns>.<agent>` */
export function chatSubject(namespace: string, agent: string): string {
	return `${CHAT_PREFIX}.${requireNamespace(namespace)}.${requireToken(agent, 'agent')}`;
}

/** Every chat event in one namespace: `kubemoot.chat.<ns>.>` */
export function chatNamespaceFilter(namespace: string): string {
	return `${CHAT_PREFIX}.${requireNamespace(namespace)}.>`;
}

/** Parse `kubemoot.chat.<ns>.<agent>`, or null when malformed. */
export function parseChatSubject(subject: string | undefined | null): ChatSubject | null {
	if (typeof subject !== 'string') return null;
	const parts = subject.split('.');
	if (parts.length !== 4 || `${parts[0]}.${parts[1]}` !== CHAT_PREFIX) return null;
	const [, , namespace, agent] = parts;
	if (!isNamespace(namespace) || !isToken(agent)) return null;
	return { namespace, agent };
}

// ---------------------------------------------------------------- agent state

/** kubemoot_agent_state key: `<ns>.<agent>` */
export function agentStateKey(namespace: string, agent: string): string {
	return `${requireNamespace(namespace)}.${requireToken(agent, 'agent')}`;
}

/** Parse a kubemoot_agent_state key, or null when malformed. */
export function parseAgentStateKey(key: string | undefined | null): ChatSubject | null {
	if (typeof key !== 'string') return null;
	const parts = key.split('.');
	if (parts.length !== 2 || !isNamespace(parts[0]) || !isToken(parts[1])) return null;
	return { namespace: parts[0], agent: parts[1] };
}

/**
 * The kubemoot_agent_state entry of the agent with this namespace AND name, or
 * undefined when absent or when either part is missing. A same-named agent in
 * another namespace never matches.
 */
export function agentStateFor<T>(
	entries: Record<string, T> | null | undefined,
	namespace: string | undefined | null,
	agent: string | undefined | null
): T | undefined {
	if (!entries || !isNamespace(namespace) || !isToken(agent)) return undefined;
	return entries[agentStateKey(namespace, agent)];
}

// ----------------------------------------------------------------- crew memory

/** kubemoot_crew_memory key prefix of one crew: `<ns>.<crew>.` */
export function crewMemoryPrefix(scope: CrewScope): string {
	const s = checkedScope(scope);
	return `${s.namespace}.${s.crew}.`;
}

/** kubemoot_crew_memory key: `<ns>.<crew>.<topic>.<key>` */
export function crewMemoryKey(scope: CrewScope, topic: string, key: string): string {
	return `${crewMemoryPrefix(scope)}${requireToken(topic, 'topic')}.${requireToken(key, 'key')}`;
}

/** Parse `<ns>.<crew>.<topic>.<key>`, or null when malformed. */
export function parseCrewMemoryKey(raw: string | undefined | null): CrewMemoryKey | null {
	if (typeof raw !== 'string') return null;
	const parts = raw.split('.');
	if (parts.length !== 4) return null;
	const [namespace, crew, topic, key] = parts;
	if (!isNamespace(namespace) || ![crew, topic, key].every(isToken)) return null;
	return { namespace, crew, topic, key };
}

/** The KV-backing stream subject that holds every key with the given prefix. */
export function kvPrefixSubject(bucket: string, keyPrefix: string): string {
	return `$KV.${requireToken(bucket, 'bucket')}.${keyPrefix}>`;
}

// -------------------------------------------------------- discussion artifacts

/** Object key in kubemoot_discussion_artifacts: `<ns>/<crew>/<thread>/<agent>/<name>` */
export function artifactObjectKey(scope: CrewScope, threadId: string, agent: string, name: string): string {
	const s = checkedScope(scope);
	const rest = [threadId, agent, name];
	if (!rest.every(isSegment)) throw new Error(`invalid artifact key segment in ${JSON.stringify(rest)}`);
	return [s.namespace, s.crew, ...rest].join('/');
}

/**
 * Parse `<ns>/<crew>/<thread>/<agent>/<signal>-<uuid>`, or null when malformed.
 * Rejects `..`, leading or trailing slashes, and any segment outside the token
 * alphabet, so a crafted key cannot read outside the artifact namespace.
 */
export function parseArtifactKey(raw: string | undefined | null): ArtifactKey | null {
	if (typeof raw !== 'string' || raw.includes('..')) return null;
	const parts = raw.split('/');
	if (parts.length !== 5) return null;
	const [namespace, crew, threadId, agent, name] = parts;
	if (!isNamespace(namespace) || !isToken(crew) || ![threadId, agent, name].every(isSegment)) return null;
	return { namespace, crew, threadId, agent, name };
}

// ------------------------------------------------------------------ topology

/** Graph node id for an agent: unique across namespaces. */
export function agentNodeId(namespace: string, agent: string): string {
	return `${namespace}/${agent}`;
}
