import { describe, expect, it } from 'vitest';
import {
	CREW_DISPLAY_NAME_ANNOTATION,
	crewDisplayName,
	crewEntry,
	crewLabel,
	crewTechnicalHint,
	crewTooltip,
	entryLabel,
	technicalNameHint,
	type CrewEntry,
	type CrewNameSource
} from './crew-display-name';

function crew(name: string | undefined, displayName?: unknown): CrewNameSource {
	const annotations =
		displayName === undefined ? undefined : ({ [CREW_DISPLAY_NAME_ANNOTATION]: displayName } as unknown as Record<string, string>);
	return { metadata: { name, annotations } };
}

describe('CREW_DISPLAY_NAME_ANNOTATION', () => {
	it('is the kubemoot.ai display-name annotation key', () => {
		expect(CREW_DISPLAY_NAME_ANNOTATION).toBe('kubemoot.ai/display-name');
	});
});

describe('crewDisplayName', () => {
	it('uses the annotation when present', () => {
		expect(crewDisplayName(crew('homelab-health-guide', 'Homelab Health Guide'))).toBe('Homelab Health Guide');
	});

	it('keeps punctuation and quotes in the annotation as written', () => {
		expect(crewDisplayName(crew('lab-ops', 'Lab-Ops "Crew" #2'))).toBe('Lab-Ops "Crew" #2');
	});

	it('trims surrounding whitespace from the annotation', () => {
		expect(crewDisplayName(crew('lab-ops', '  Lab Ops \t'))).toBe('Lab Ops');
	});

	it('falls back to the technical name when the annotation is absent', () => {
		expect(crewDisplayName(crew('homelab-health-guide'))).toBe('homelab-health-guide');
		expect(crewDisplayName({ metadata: { name: 'lab-ops', annotations: { other: 'x' } } })).toBe('lab-ops');
		expect(crewDisplayName({ metadata: { name: 'lab-ops', annotations: null } })).toBe('lab-ops');
	});

	it('falls back to the technical name when the annotation is blank or whitespace', () => {
		expect(crewDisplayName(crew('lab-ops', ''))).toBe('lab-ops');
		expect(crewDisplayName(crew('lab-ops', '   \n\t'))).toBe('lab-ops');
	});

	it('falls back to the technical name when the annotation is not a string', () => {
		expect(crewDisplayName(crew('lab-ops', 42))).toBe('lab-ops');
		expect(crewDisplayName(crew('lab-ops', null))).toBe('lab-ops');
	});

	it('is empty when there is no metadata or no crew', () => {
		expect(crewDisplayName({})).toBe('');
		expect(crewDisplayName({ metadata: null })).toBe('');
		expect(crewDisplayName(null)).toBe('');
		expect(crewDisplayName(undefined)).toBe('');
	});

	it('uses the annotation even without a technical name', () => {
		expect(crewDisplayName(crew(undefined, 'Nameless'))).toBe('Nameless');
	});
});

describe('technicalNameHint', () => {
	it('is the technical name when it differs from the display name', () => {
		expect(technicalNameHint('Homelab Health Guide', 'homelab-health-guide')).toBe('homelab-health-guide');
	});

	it('is undefined when the names are equal or the technical name is empty', () => {
		expect(technicalNameHint('lab-ops', 'lab-ops')).toBeUndefined();
		expect(technicalNameHint('Lab Ops', '')).toBeUndefined();
	});
});

describe('crewTechnicalHint', () => {
	it('is the technical name when the display name differs', () => {
		expect(crewTechnicalHint(crew('lab-ops', 'Lab Ops'))).toBe('lab-ops');
	});

	it('is undefined when the display name equals the technical name', () => {
		expect(crewTechnicalHint(crew('lab-ops'))).toBeUndefined();
		expect(crewTechnicalHint(crew('lab-ops', 'lab-ops'))).toBeUndefined();
		expect(crewTechnicalHint(crew('lab-ops', '  lab-ops  '))).toBeUndefined();
		expect(crewTechnicalHint(crew('lab-ops', ' '))).toBeUndefined();
	});

	it('is undefined without metadata', () => {
		expect(crewTechnicalHint(null)).toBeUndefined();
		expect(crewTechnicalHint({})).toBeUndefined();
	});
});

const entries: CrewEntry[] = [
	{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' },
	{ namespace: 'guide', crew: 'homelab-health-guide', displayName: 'homelab-health-guide' },
	{ namespace: 'broken', crew: 'lab-ops', displayName: '' }
];

describe('crewEntry', () => {
	it('finds the entry by namespace alone', () => {
		expect(crewEntry(entries, 'pilot')).toBe(entries[0]);
		expect(crewEntry(entries, 'pilot', null)).toBe(entries[0]);
	});

	it('finds the entry by namespace and technical name', () => {
		expect(crewEntry(entries, 'guide', 'homelab-health-guide')).toBe(entries[1]);
	});

	it('is undefined for another crew, another namespace, or a missing list', () => {
		expect(crewEntry(entries, 'pilot', 'homelab-health-guide')).toBeUndefined();
		expect(crewEntry(entries, 'other')).toBeUndefined();
		expect(crewEntry(entries, '')).toBeUndefined();
		expect(crewEntry(entries, null)).toBeUndefined();
		expect(crewEntry(null, 'pilot')).toBeUndefined();
		expect(crewEntry(undefined, 'pilot')).toBeUndefined();
	});
});

describe('entryLabel', () => {
	it('is the display name, else the technical name', () => {
		expect(entryLabel(entries[0])).toBe('Homelab Pilot');
		expect(entryLabel(entries[1])).toBe('homelab-health-guide');
		expect(entryLabel(entries[2])).toBe('lab-ops');
	});
});

describe('crewLabel', () => {
	it('is the display name of the crew with this namespace and name', () => {
		expect(crewLabel(entries, 'pilot', 'homelab-pilot')).toBe('Homelab Pilot');
		expect(crewLabel(entries, 'guide', 'homelab-health-guide')).toBe('homelab-health-guide');
	});

	it('never takes a same-named crew from another namespace', () => {
		expect(crewLabel(entries, 'other', 'homelab-pilot')).toBe('homelab-pilot');
	});

	it('is the technical name for an unknown crew, a missing list, or a blank entry', () => {
		expect(crewLabel(entries, 'pilot', 'unknown')).toBe('unknown');
		expect(crewLabel([], 'pilot', 'homelab-pilot')).toBe('homelab-pilot');
		expect(crewLabel(null, 'pilot', 'homelab-pilot')).toBe('homelab-pilot');
		expect(crewLabel(undefined, undefined, 'homelab-pilot')).toBe('homelab-pilot');
		expect(crewLabel(entries, 'broken', 'lab-ops')).toBe('lab-ops');
	});
});

describe('crewTooltip', () => {
	it('names the technical crew and the namespace when the label differs', () => {
		expect(crewTooltip(entries, 'pilot', 'homelab-pilot')).toBe('crew homelab-pilot, namespace pilot');
	});

	it('names only the namespace when the label is the technical name', () => {
		expect(crewTooltip(entries, 'guide', 'homelab-health-guide')).toBe('namespace guide');
		expect(crewTooltip(null, 'pilot', 'homelab-pilot')).toBe('namespace pilot');
	});

	it('is undefined when there is nothing to add', () => {
		expect(crewTooltip(entries, undefined, 'homelab-pilot')).toBeUndefined();
		expect(crewTooltip(entries, '', 'homelab-pilot')).toBeUndefined();
	});
});
