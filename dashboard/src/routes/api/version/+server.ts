import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { readFileSync } from 'fs';
import { join } from 'path';

function getVersion(): string {
	// First try APP_VERSION env var (set by Helm chart)
	if (process.env.APP_VERSION) {
		return process.env.APP_VERSION;
	}

	// Try reading VERSION file
	try {
		const versionPath = join(process.cwd(), 'VERSION');
		return readFileSync(versionPath, 'utf-8').trim();
	} catch {
		// Fall back to package.json version
		try {
			const pkgPath = join(process.cwd(), 'package.json');
			const pkg = JSON.parse(readFileSync(pkgPath, 'utf-8'));
			return pkg.version || 'dev';
		} catch {
			return 'dev';
		}
	}
}

export const GET: RequestHandler = async () => {
	return json({
		version: getVersion(),
		buildTime: process.env.BUILD_TIME,
		gitCommit: process.env.GIT_COMMIT
	});
};
