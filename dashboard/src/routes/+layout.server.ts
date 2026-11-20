import type { LayoutServerLoad } from './$types';
import { dashboardMode } from '#lib/server/mode.js';

// The deployment mode for the browser: scoped limits what the namespace-aware pages offer.
export const load: LayoutServerLoad = () => dashboardMode();
