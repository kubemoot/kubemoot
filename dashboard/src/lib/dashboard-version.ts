// The dashboard's own version, as the sidebar and the exported discussions show it.
// When the version call fails the page says so; it never claims a "dev" build.

export const VERSION_UNKNOWN = 'unknown';

type Fetcher = (url: string) => Promise<Response>;

/** The version the server reports, or VERSION_UNKNOWN when the call fails or answers without one. */
export async function fetchDashboardVersion(fetcher: Fetcher, url: string): Promise<string> {
	try {
		const res = await fetcher(url);
		if (!res.ok) return VERSION_UNKNOWN;
		const data = await res.json();
		return typeof data?.version === 'string' && data.version ? data.version : VERSION_UNKNOWN;
	} catch {
		return VERSION_UNKNOWN;
	}
}

/** The version as shown: "v1.2.3", or "version unknown" when it could not be read. */
export function versionLabel(version: string): string {
	return version === VERSION_UNKNOWN ? 'version unknown' : `v${version}`;
}

/** The Kubemoot release as shown: "v1.2.3", or "not reported" when the operator has not written one. */
export function releaseLabel(release: string): string {
	return release ? `v${release}` : 'not reported';
}
