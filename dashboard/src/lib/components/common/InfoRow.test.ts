import { render, screen } from '@testing-library/svelte';
import { describe, it, expect } from 'vitest';
import InfoRow from './InfoRow.svelte';

// Seed component test establishing the dashboard's headless (jsdom) test path.
describe('InfoRow', () => {
	it('renders the label and a provided value', () => {
		render(InfoRow, { props: { label: 'Node', value: 'gpu-worker-1' } });
		expect(screen.getByText('Node')).toBeTruthy();
		expect(screen.getByText('gpu-worker-1')).toBeTruthy();
	});

	it('falls back to a dash when the value is null', () => {
		render(InfoRow, { props: { label: 'Node', value: null } });
		expect(screen.getByText('-')).toBeTruthy();
	});

	it('renders a numeric value', () => {
		render(InfoRow, { props: { label: 'Replicas', value: 3 } });
		expect(screen.getByText('3')).toBeTruthy();
	});
});
