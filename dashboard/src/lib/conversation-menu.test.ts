import { describe, expect, it, vi } from 'vitest';
import { conversationMenu, type MenuContext } from './conversation-menu.js';

const base = (over: Partial<MenuContext> = {}): MenuContext => ({
	scope: { namespace: 'ns', crew: 'pilot' },
	conversationId: 'c-1',
	run: { running: false, paused: false },
	showDetails: false,
	downloadHref: '/export?id=c-1',
	discussionHref: '/moot?thread=t1',
	ontoggledetails: vi.fn(),
	copyConversation: vi.fn(),
	copyIdAndQuestion: vi.fn(),
	copyKmctl: vi.fn(),
	control: vi.fn(),
	remove: vi.fn(),
	...over
});
const labels = (c: MenuContext) => conversationMenu(c).flatMap((e) => (e.kind === 'separator' ? [] : [e.label]));

describe('conversationMenu', () => {
	it('offers copy, download, the Moot, kmctl and delete for a finished conversation, and no control', () => {
		expect(labels(base())).toEqual([
			'Show answer details',
			'Copy conversation',
			'Copy conversation id with the first question',
			'Download conversation',
			'Open in the Moot',
			'Copy kmctl command',
			'Delete conversation'
		]);
	});

	it('offers Pause and Stop while it runs', () => {
		const l = labels(base({ run: { running: true, paused: false } }));
		expect(l).toContain('Pause');
		expect(l).toContain('Stop');
		expect(l).not.toContain('Resume');
	});

	it('offers Resume instead of Pause while it is paused', () => {
		const l = labels(base({ run: { running: true, paused: true } }));
		expect(l).toContain('Resume');
		expect(l).toContain('Stop');
		expect(l).not.toContain('Pause');
	});

	it('leaves out what needs an id or a crew', () => {
		const l = labels(base({ conversationId: undefined, downloadHref: null, run: { running: true, paused: false } }));
		expect(l).toEqual(['Show answer details', 'Copy conversation', 'Open in the Moot']);
		expect(labels(base({ scope: null }))).not.toContain('Delete conversation');
	});

	it('runs the callback of the entry chosen', () => {
		const c = base({ run: { running: true, paused: false } });
		const entries = conversationMenu(c);
		for (const e of entries) {
			if (e.kind === 'item' && e.label === 'Stop') e.onselect();
			if (e.kind === 'item' && e.label === 'Delete conversation') e.onselect();
		}
		expect(c.control).toHaveBeenCalledWith('stop');
		expect(c.remove).toHaveBeenCalled();
	});

	it('marks delete as dangerous and shows the details switch as it is', () => {
		const entries = conversationMenu(base({ showDetails: true }));
		expect(entries.find((e) => e.kind === 'item' && e.label === 'Delete conversation')).toMatchObject({ danger: true });
		expect(entries[0]).toMatchObject({ kind: 'check', checked: true });
	});
});
