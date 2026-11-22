import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { CrewEntry } from '#lib/crew-display-name.js';
import NamespaceSelector from './NamespaceSelector.svelte';

function crewOptions(container: HTMLElement): HTMLOptionElement[] {
	return [...container.querySelectorAll('option')].filter((o) => o.value !== '' && !o.disabled);
}

describe('NamespaceSelector', () => {
	it('shows the display name alone, with the technical name in the tooltip, when they differ', () => {
		const crews: CrewEntry[] = [{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' }];
		const { container } = render(NamespaceSelector, { props: { crews } });
		const [option] = crewOptions(container);
		expect(option.value).toBe('pilot');
		expect(option.textContent).toBe('Homelab Pilot');
		expect(option.title).toBe('homelab-pilot');
	});

	it('shows the technical name when there is no separate display name, without the namespace', () => {
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

	it('names the namespace only when two crews share a name', () => {
		const crews: CrewEntry[] = [
			{ namespace: 'gav-platform-ops', crew: 'platform-ops', displayName: 'platform-ops' },
			{ namespace: 'otters-platform-ops', crew: 'platform-ops', displayName: 'platform-ops' }
		];
		const { container } = render(NamespaceSelector, { props: { crews } });
		const options = crewOptions(container);
		expect(options.map((o) => o.textContent)).toEqual([
			'platform-ops [gav-platform-ops]',
			'platform-ops [otters-platform-ops]'
		]);
		expect(new Set(options.map((o) => o.textContent)).size).toBe(2);
	});

	it('leaves the namespace off the names that are not shared', () => {
		const crews: CrewEntry[] = [
			{ namespace: 'a', crew: 'pilot', displayName: 'Pilot' },
			{ namespace: 'b', crew: 'ops', displayName: 'Ops' },
			{ namespace: 'c', crew: 'ops', displayName: 'Ops' }
		];
		const { container } = render(NamespaceSelector, { props: { crews } });
		expect(crewOptions(container).map((o) => o.textContent)).toEqual(['Pilot', 'Ops [b]', 'Ops [c]']);
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

	it('lists two crews of one namespace without a duplicate key', () => {
		const crews: CrewEntry[] = [
			{ namespace: 'ns', crew: 'a', displayName: 'Alpha' },
			{ namespace: 'ns', crew: 'b', displayName: 'Beta' }
		];
		const { container } = render(NamespaceSelector, { props: { crews } });
		expect(crewOptions(container).map((o) => o.textContent)).toEqual(['Alpha', 'Beta']);
	});
});
