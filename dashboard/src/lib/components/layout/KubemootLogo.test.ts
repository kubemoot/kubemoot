import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import KubemootLogo from './KubemootLogo.svelte';

describe('KubemootLogo', () => {
	it('shows the white-text horizontal lockup, named for screen readers', () => {
		render(KubemootLogo);
		const img = screen.getByRole('img', { name: 'Kubemoot' });
		expect(img.getAttribute('src')).toBe('/kubemoot-horizontal-white-text.svg');
	});

	it('links to the project site in a new tab without handing over the opener', () => {
		render(KubemootLogo);
		const link = screen.getByRole('link', { name: 'Kubemoot' });
		expect(link.getAttribute('href')).toBe('https://kubemoot.org');
		expect(link.getAttribute('target')).toBe('_blank');
		expect(link.getAttribute('rel')).toContain('noopener');
	});
});
