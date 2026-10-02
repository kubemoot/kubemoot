// The one rule for the human-friendly name of a crew. A Crew CR may carry the
// annotation `kubemoot.ai/display-name` (any one line of text). The dashboard
// shows it wherever people read crew names, and falls back to metadata.name
// (the technical name) when the annotation is absent or blank. The technical
// name stays the identifier for routes, API calls, NATS subjects, and keys.

/** Crew CR annotation that holds the crew's human-friendly name. */
export const CREW_DISPLAY_NAME_ANNOTATION = 'kubemoot.ai/display-name';

/** The parts of a Crew CR that identify and name it. */
export interface CrewNameSource {
	metadata?: {
		name?: string;
		namespace?: string;
		annotations?: Record<string, string> | null;
	} | null;
}

/** A crew in a namespace, with the name people read. */
export interface CrewEntry {
	namespace: string;
	crew: string;
	displayName: string;
}

/** The trimmed annotation value, or '' when it is absent, blank, or not a string. */
function annotationValue(crew: CrewNameSource | null | undefined): string {
	const value: unknown = crew?.metadata?.annotations?.[CREW_DISPLAY_NAME_ANNOTATION];
	return typeof value === 'string' ? value.trim() : '';
}

/** The crew's display name, falling back to its technical name (metadata.name). */
export function crewDisplayName(crew: CrewNameSource | null | undefined): string {
	return annotationValue(crew) || (crew?.metadata?.name ?? '');
}

/**
 * The technical name to show beside a display name, or undefined when the two
 * are the same (nothing extra to show).
 */
export function technicalNameHint(displayName: string, technicalName: string): string | undefined {
	return technicalName && displayName !== technicalName ? technicalName : undefined;
}

/** The technical-name hint for a Crew CR (see technicalNameHint). */
export function crewTechnicalHint(crew: CrewNameSource | null | undefined): string | undefined {
	return technicalNameHint(crewDisplayName(crew), crew?.metadata?.name ?? '');
}

/**
 * The crew entry in this namespace, or undefined when there is none. When a
 * technical name is given, the entry must also have that name, so a crew in
 * another namespace or a different crew never matches.
 */
export function crewEntry(
	entries: readonly CrewEntry[] | null | undefined,
	namespace: string | null | undefined,
	crew?: string | null
): CrewEntry | undefined {
	return entries?.find((e) => e.namespace === namespace && (crew == null || e.crew === crew));
}

/** The name people read for a crew entry: its display name, else its technical name. */
export function entryLabel(entry: CrewEntry): string {
	return entry.displayName || entry.crew;
}

/**
 * The display name of the crew with this namespace and technical name, looked up
 * in a crew list; the technical name itself when the crew is not in the list.
 */
export function crewLabel(
	entries: readonly CrewEntry[] | null | undefined,
	namespace: string | null | undefined,
	crew: string
): string {
	const entry = crewEntry(entries, namespace, crew);
	return entry ? entryLabel(entry) : crew;
}

/**
 * Tooltip text for a crew label: the technical name when it differs from the
 * label, then the namespace when known. Undefined when there is nothing to add.
 */
export function crewTooltip(
	entries: readonly CrewEntry[] | null | undefined,
	namespace: string | null | undefined,
	crew: string
): string | undefined {
	const parts: string[] = [];
	const hint = technicalNameHint(crewLabel(entries, namespace, crew), crew);
	if (hint) parts.push(`crew ${hint}`);
	if (namespace) parts.push(`namespace ${namespace}`);
	return parts.length > 0 ? parts.join(', ') : undefined;
}
