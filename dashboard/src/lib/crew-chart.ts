// The Helm chart a crew was installed from, read from its labels.
// `helm.sh/chart` is shaped like `<chart>-<semver>` (e.g. `homelab-pilot-crew-0.91.0`).

const CHART_VERSION_SUFFIX = /-(\d[^-]*)$/;

type Labels = Record<string, string> | undefined;

/** The chart version from helm.sh/chart, else app.kubernetes.io/version, else null. */
export function chartVersion(labels: Labels): string | null {
	const helmChart = labels?.['helm.sh/chart'];
	const m = helmChart ? CHART_VERSION_SUFFIX.exec(helmChart) : null;
	if (m) return m[1];
	return labels?.['app.kubernetes.io/version'] ?? null;
}

/** The chart name: helm.sh/chart without its trailing version, or null without the label. */
export function chartName(labels: Labels): string | null {
	const helmChart = labels?.['helm.sh/chart'];
	return helmChart ? helmChart.replace(CHART_VERSION_SUFFIX, '') : null;
}
