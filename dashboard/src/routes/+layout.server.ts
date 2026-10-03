import type { LayoutServerLoad } from './$types';
import { dashboardMode } from '$lib/server/mode';

// The deployment mode for the browser: read-only hides the write controls, scoped
// limits what the namespace-aware pages offer.
export const load: LayoutServerLoad = () => dashboardMode();
