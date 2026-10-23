// How an MCP server shows the credential it is reached with. The dashboard names
// the Secret that holds the key and never reads or shows the key itself.

import type { MCPServerSpec } from '#lib/types/kubemoot.js';

/** "key from Secret <name>" when the server is reached with a key held in a Secret, else null. */
export function authDisplay(spec: Pick<MCPServerSpec, 'registry'> | undefined): string | null {
	const name = spec?.registry?.authSecretRef?.trim();
	return name ? `key from Secret ${name}` : null;
}
