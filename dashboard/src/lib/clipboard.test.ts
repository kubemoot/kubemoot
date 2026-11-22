import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyText } from './clipboard';

afterEach(() => vi.unstubAllGlobals());

describe('copyText', () => {
	it('writes the text to the clipboard', async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		vi.stubGlobal('navigator', { clipboard: { writeText } });
		await copyText('hello');
		expect(writeText).toHaveBeenCalledWith('hello');
	});

	it('throws when the browser does not allow it, so no tick shows', async () => {
		vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } });
		await expect(copyText('x')).rejects.toThrow('denied');
	});
});
