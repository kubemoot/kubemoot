import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import ConversationDownload from './ConversationDownload.svelte';

const scope = { namespace: 'crew-demo', crew: 'demo' };

describe('ConversationDownload', () => {
	it('links one conversation to the export route as a download', () => {
		render(ConversationDownload, { scope, ids: ['abc'], endpoint: '/api/kubemoot/conversation-export', timeZone: 'UTC' });
		const link = screen.getByRole('link', { name: 'Download conversation' });
		expect(link.getAttribute('href')).toBe('/api/kubemoot/conversation-export?namespace=crew-demo&crew=demo&id=abc&timeZone=UTC');
		expect(link.hasAttribute('download')).toBe(true);
	});

	it('says how many when several are selected, with one id each', () => {
		render(ConversationDownload, { scope, ids: ['a', 'b'], endpoint: '/e' });
		const link = screen.getByRole('link', { name: 'Download 2 conversations' });
		expect(new URL(link.getAttribute('href') ?? '', 'https://x').searchParams.getAll('id')).toEqual(['a', 'b']);
	});

	it('uses the label it is given', () => {
		render(ConversationDownload, { scope, ids: ['a'], endpoint: '/e', label: 'Download' });
		expect(screen.getByRole('link', { name: 'Download' })).toBeTruthy();
	});

	it('shows nothing without a crew or without conversations', () => {
		render(ConversationDownload, { scope: null, ids: ['a'], endpoint: '/e' });
		render(ConversationDownload, { scope, ids: [], endpoint: '/e' });
		expect(screen.queryByRole('link')).toBeNull();
	});
});
