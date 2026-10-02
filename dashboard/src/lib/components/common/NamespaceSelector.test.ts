import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { CrewEntry } from '$lib/crew-display-name';
import NamespaceSelector from './NamespaceSelector.svelte';

function crewOptions(container: HTMLElement): HTMLOptionElement[] {
	return [...container.querySelectorAll('option')].filter((o) => o.value !== '' && !o.disabled);
}

describe('NamespaceSelector', () => {
	it('shows the display name with the technical name beside it when they differ', () => {
		const crews: CrewEntry[] = [{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' }];
		const { container } = render(NamespaceSelector, { props: { crews } });
		const [option] = crewOptions(container);
		expect(option.value).toBe('pilot');
		expect(option.textContent).toBe('Homelab Pilot (homelab-pilot)');
		expect(option.title).toBe('homelab-pilot');
	});

	it('shows only the technical name when there is no separate display name', () => {
		const crews: CrewEntry[] = [
			{ namespace: 'guide', crew: 'homelab-health-guide', displayName: 'homelab-health-guide' },
			{ namespace: 'blank', crew: 'lab-ops', displayName: '' }
		];
		const { container } = render(NamespaceSelector, { props: { crews } });
		const options = crewOptions(container);
		expect(options.map((o) => o.textContent)).toEqual(['homelab-health-guide', 'lab-ops']);
		expect(options.map((o) => o.value)).toEqual(['guide', 'blank']);
		expect(options.every((o) => !o.hasAttribute('title'))).toBe(true);
	});

	it('shows only All crews when there are no crews', () => {
		const { container } = render(NamespaceSelector, { props: { crews: [] } });
		expect(crewOptions(container)).toEqual([]);
		expect(container.querySelector('option')?.textContent).toBe('All crews');
	});

	it('shows a loading state instead of the selector', () => {
		const { container } = render(NamespaceSelector, { props: { crews: [], loading: true } });
		expect(container.querySelector('select')).toBeNull();
		expect(container.textContent).toContain('Loading...');
	});
});
