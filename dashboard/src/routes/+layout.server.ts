import type { LayoutServerLoad } from './$types';
import { dashboardMode } from '#lib/server/mode.js';

// The deployment mode for the browser (scoped limits what the namespace-aware pages offer), and
// who is signed in: the username only, never the token.
export const load: LayoutServerLoad = ({ locals }) => ({ ...dashboardMode(), user: locals.user });
