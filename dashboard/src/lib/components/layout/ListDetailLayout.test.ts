import { render, screen } from '@testing-library/svelte';
import { readFileSync } from 'node:fs';
import { createRawSnippet } from 'svelte';
import { describe, expect, it } from 'vitest';
import ListDetailLayout from './ListDetailLayout.svelte';
import ListCard from './ListCard.svelte';
import DetailHeader from './DetailHeader.svelte';

const text = (content: string) => createRawSnippet(() => ({ render: () => `<span>${content}</span>` }));

describe('ListDetailLayout', () => {
	it('puts the title and the one muted line top-left, the list in one column and the open item in the other', () => {
		const { container } = render(ListDetailLayout, { route: '/conversations', subtitle: 'A line.', list: text('the list'), detail: text('the open item') });
		expect(screen.getByRole('heading', { name: 'Conversations' })).toBeTruthy();
		expect(screen.getByText('A line.')).toBeTruthy();
		const header = container.querySelector('.page-header');
		const columns = container.querySelector('.columns');
		expect(header?.nextElementSibling).toBe(columns);
		expect(columns?.querySelector('.list')?.textContent).toContain('the list');
		expect(columns?.querySelector('.detail')?.textContent).toContain('the open item');
	});

	it('shows the page actions only when there are some', () => {
		const without = render(ListDetailLayout, { route: '/conversations', subtitle: 's', list: text('l'), detail: text('d') });
		expect(without.container.querySelector('.header-actions')).toBeNull();
		without.unmount();
		const withActions = render(ListDetailLayout, { route: '/conversations', subtitle: 's', actions: text('Connected'), list: text('l'), detail: text('d') });
		expect(withActions.container.querySelector('.header-actions')?.textContent).toContain('Connected');
	});

	it('stacks the list above the open item on a narrow screen', () => {
		const css = readFileSync('src/lib/components/layout/ListDetailLayout.svelte', 'utf8');
		const narrow = /@media \(max-width: 1100px\) \{[\s\S]*\}/.exec(css)?.[0] ?? '';
		expect(narrow).toMatch(/\.columns \{ flex-direction: column/);
	});
});

describe('the pages that share it', () => {
	it.each(['src/routes/conversations/+page.svelte', 'src/routes/moot/+page.svelte'])('%s uses the layout, the card and the header, and carries none of its own', (file) => {
		const source = readFileSync(file, 'utf8');
		expect(source).toContain('<ListDetailLayout');
		expect(source).toContain('<ListCard');
		expect(source).toContain('<FilterBar');
		const style = source.slice(source.indexOf('<style>'));
		expect(style).not.toMatch(/\.(page-header|columns|list-detail|thread-card|chat-row|sidebar-panel|main-panel|thread-list|thread-detail)(?![\w-])/);
	});

	it('has no "Recent" label above the Conversations list', () => {
		const source = readFileSync('src/routes/conversations/+page.svelte', 'utf8');
		expect(source).not.toMatch(/>\s*Recent\s*</);
	});
});

describe('ListCard', () => {
	it('marks the open card and reports a click', async () => {
		let clicked = 0;
		const { container } = render(ListCard, { selected: true, kind: 'conversation', onopen: () => clicked++, children: text('Why?') });
		const card = container.querySelector('.card') as HTMLElement;
		expect(card.classList.contains('selected')).toBe(true);
		card.click();
		expect(clicked).toBe(1);
	});
});

describe('DetailHeader', () => {
	it('puts the state on the left and the actions on the right', () => {
		const { container } = render(DetailHeader, { children: text('Closed'), actions: text('menu') });
		const [lead, end] = [container.querySelector('.lead'), container.querySelector('.end')];
		expect(lead?.textContent).toContain('Closed');
		expect(end?.textContent).toContain('menu');
		expect(lead?.nextElementSibling).toBe(end);
	});
});

describe('ListDetailLayout with nothing open', () => {
	it('marks the layout so a narrow screen gives the list the whole height', () => {
		const text2 = createRawSnippet(() => ({ render: () => '<span>x</span>' }));
		const open = render(ListDetailLayout, { route: '/conversations', subtitle: 's', list: text2, detail: text2 });
		expect(open.container.querySelector('.list-detail')?.classList.contains('no-detail')).toBe(false);
		open.unmount();
		const none = render(ListDetailLayout, { route: '/conversations', subtitle: 's', list: text2, detail: text2, detailOpen: false });
		expect(none.container.querySelector('.list-detail')?.classList.contains('no-detail')).toBe(true);
		const css = readFileSync('src/lib/components/layout/ListDetailLayout.svelte', 'utf8');
		expect(css).toMatch(/\.no-detail \.list \{ max-height: none; flex: 1; \}/);
		expect(css).toMatch(/\.no-detail \.detail \{ display: none; \}/);
	});
});
