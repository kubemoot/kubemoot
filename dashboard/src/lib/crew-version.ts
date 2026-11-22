// The crew chart version as a person reads it. A crew's version label comes from a
// Kubernetes label, where a build suffix "+<build>" is written "_<build>", so the raw
// value reads like 0.51.0-rc.2_5776d8b4e1a8.

const HEX_BUILD = /^[0-9a-f]{6,}$/i;

/** The version without its build suffix: a "+build" tail, or a trailing "_<hex>" label form of one. */
export function formatCrewVersion(raw: string | null | undefined): string {
	if (!raw) return '';
	let version = raw.trim();
	if (version.startsWith('v') && version.length > 1 && version[1] >= '0' && version[1] <= '9') version = version.slice(1);
	const plus = version.indexOf('+');
	if (plus >= 0) version = version.slice(0, plus);
	const under = version.lastIndexOf('_');
	if (under >= 0 && HEX_BUILD.test(version.slice(under + 1))) version = version.slice(0, under);
	return version;
}

/** The tooltip text: the clean version, with the full label value when it differs. */
export function crewVersionTooltip(raw: string | null | undefined): string {
	const clean = formatCrewVersion(raw);
	if (!clean) return '';
	return raw && raw !== clean ? `Crew version ${clean} (label value ${raw})` : `Crew version ${clean}`;
}
