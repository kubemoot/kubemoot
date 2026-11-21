import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { NatsStatus, StoreStatus } from '#lib/nats/status.js';
import NatsTile from './NatsTile.svelte';

function store(name: string, bytes: number, maxBytes: number): StoreStatus {
	return {
		definition: null,
		name,
		kind: 'stream',
		limits: { maxAgeSeconds: null, maxBytes, maxMessages: null, history: null },
		usage: { messages: 10, bytes, firstSeq: 1, lastSeq: 10, consumers: 0, keys: null }
	};
}

const connected: NatsStatus = {
	connected: true,
	checkedAt: 'now',
	server: { version: '2.12.0', name: 'n', uptime: null, connections: null },
	account: {
		storage: { used: 268435456, limit: 1073741824 },
		memory: { used: 0, limit: null },
		streams: { used: 1, limit: null },
		consumers: { used: 0, limit: null }
	},
	stores: [store('KUBEMOOT_DISCUSS', 100, 1000)],
	absent: []
};

describe('NatsTile', () => {
	it('shows a spinner until the first read lands', () => {
		const { getByRole } = render(NatsTile, { status: null, href: '/dashboard/nats' });
		expect(getByRole('status')).toBeTruthy();
	});

	it('shows version, storage against its limit, and totals when connected', () => {
		const { getByTestId, getByText, queryByTestId } = render(NatsTile, { status: connected, href: '/dashboard/nats' });
		expect(getByText('Connected')).toBeTruthy();
		expect(getByText('Server 2.12.0')).toBeTruthy();
		expect(getByTestId('nats-storage').textContent).toBe('256 MiB of 1.0 GiB (25%)');
		expect(getByTestId('nats-memory').textContent).toContain('no limit');
		expect(queryByTestId('nats-warnings')).toBeNull();
	});

	it('says so and gives the reason when NATS is not connected', () => {
		const { getByText, queryByTestId } = render(NatsTile, {
			status: { connected: false, error: 'connection refused', checkedAt: 'now', stores: [], absent: [] },
			href: '/dashboard/nats'
		});
		expect(getByText('Not connected')).toBeTruthy();
		expect(getByText('connection refused')).toBeTruthy();
		expect(queryByTestId('nats-storage')).toBeNull();
	});

	it('shows a warning band when a store is near a limit', () => {
		const { getByText, getByTestId } = render(NatsTile, {
			status: { ...connected, stores: [store('KUBEMOOT_DISCUSS', 850, 1000)] },
			href: '/dashboard/nats'
		});
		expect(getByText('Near a limit')).toBeTruthy();
		expect(getByTestId('nats-warnings').textContent).toContain('KUBEMOOT_DISCUSS: 85% of its bytes limit');
	});

	it('links to the NATS status page', () => {
		const { getByTestId } = render(NatsTile, { status: connected, href: '/dashboard/nats' });
		expect(getByTestId('nats-tile').getAttribute('href')).toBe('/dashboard/nats');
	});
});
