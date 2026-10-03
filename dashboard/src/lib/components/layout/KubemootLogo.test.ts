import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import KubemootLogo from './KubemootLogo.svelte';

describe('KubemootLogo', () => {
	it('shows the white-text horizontal lockup, named for screen readers', () => {
		render(KubemootLogo);
		const img = screen.getByRole('img', { name: 'Kubemoot' });
		expect(img.getAttribute('src')).toBe('/kubemoot-horizontal-white-text.svg');
	});
});
