import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { MCPServer } from '#lib/types/kubemoot.js';
import MCPServerCard from './MCPServerCard.svelte';

function serverWith(spec: Partial<MCPServer['spec']>): MCPServer {
	return {
		metadata: { name: 'search-mcp', namespace: 'crew-oguide' },
		spec: { transport: 'stdio', ...spec },
		status: { phase: 'Running', ready: true }
	} as unknown as MCPServer;
}

describe('MCPServerCard description', () => {
	it('shows what the server is for when its spec says', () => {
		const { container } = render(MCPServerCard, {
			props: { server: serverWith({ description: 'Searches the O\'Reilly catalog.' }) }
		});
		expect(container.querySelector('.description')?.textContent).toBe(
			"Searches the O'Reilly catalog."
		);
	});

	it('shows no description line when the spec has none', () => {
		const { container } = render(MCPServerCard, { props: { server: serverWith({}) } });
		expect(container.querySelector('.description')).toBeNull();
	});

	it('shows no description line for an empty description', () => {
		const { container } = render(MCPServerCard, {
			props: { server: serverWith({ description: '' }) }
		});
		expect(container.querySelector('.description')).toBeNull();
	});
});
