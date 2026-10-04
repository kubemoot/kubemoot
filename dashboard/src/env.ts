import { defineEnvVars } from '@sveltejs/kit/env';

// Environment variables the app reads through $app/env/private. Each is read once when
// the server starts. Deployment switches read on every request (read-only mode,
// namespace selector) live in src/lib/server/mode.ts and read process.env directly.
export const variables = defineEnvVars({
	OPERATOR_REPORT_URL: {
		description: 'Base URL of the operator report service; unset or empty uses the in-cluster Service',
		schema: (value) => value
	}
});
