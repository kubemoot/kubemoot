import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import AnswerPanel from './AnswerPanel.svelte';

describe('AnswerPanel', () => {
	it('shows the answer with its time and the sources note', () => {
		render(AnswerPanel, {
			state: { kind: 'answered', text: 'gpu-0: 33C', note: 'Some sources could not be reached: workspace_query.', messageId: 'a' },
			elapsed: '52.3s'
		});
		expect(screen.getByText(/gpu-0: 33C/)).toBeTruthy();
		expect(screen.getByText('52.3s')).toBeTruthy();
		expect(screen.getByRole('note').textContent).toMatch(/could not be reached/);
	});
	it('says plainly when still working, stopped or failed', () => {
		const { unmount } = render(AnswerPanel, { state: { kind: 'working' } });
		expect(screen.getByText(/still working/)).toBeTruthy();
		unmount();
		const r2 = render(AnswerPanel, { state: { kind: 'stopped' } });
		expect(screen.getByText(/stopped/)).toBeTruthy();
		r2.unmount();
		render(AnswerPanel, { state: { kind: 'failed', reason: 'model timed out' } });
		expect(screen.getByText(/failed: model timed out/)).toBeTruthy();
	});
});
