import { describe, expect, it } from 'vitest';
import { digest, rootSchema, withoutGoHints, withoutRoot } from '../../../scripts/gen-bus-types.mjs';

describe('gen-bus-types', () => {
	it('computes the same schema digest as the Go generator', () => {
		const files = new Map([
			['asyncapi.yaml', Buffer.from('info: {}')],
			['a.json', Buffer.from('{}')]
		]);
		// The same vector is pinned in operator/pkg/bus/internal/schemaset.
		expect(digest(files)).toBe('f62b4a2f2d92fcc5594f1fd182ceb78f784f1328380487a400779a9d33bc7cf7');
	});

	it('changes the digest with a file name or its content', () => {
		const base = digest(new Map([['a.json', Buffer.from('{}')]]));
		expect(digest(new Map([['b.json', Buffer.from('{}')]]))).not.toBe(base);
		expect(digest(new Map([['a.json', Buffer.from('{ }')]]))).not.toBe(base);
	});

	it('defines one type per schema file', () => {
		expect(rootSchema(['agent-heartbeat.json', 'stream-event.json'])).toEqual({
			title: 'KubemootBus',
			type: 'object',
			definitions: {
				'agent-heartbeat': { $ref: 'agent-heartbeat.json' },
				'stream-event': { $ref: 'stream-event.json' }
			}
		});
		expect(rootSchema([]).definitions).toEqual({});
	});

	it('drops the Go generator hints at every level', () => {
		const schema = JSON.stringify({
			properties: { metadata: { $ref: 'm.json', goJSONSchema: { type: 'map[string]any' } } },
			goJSONSchema: { pointer: false }
		});
		expect(JSON.parse(withoutGoHints(schema))).toEqual({ properties: { metadata: { $ref: 'm.json' } } });
		expect(() => withoutGoHints('{')).toThrow();
	});

	it('removes only the empty root interface', () => {
		const ts = 'export type A = B;\nexport interface KubemootBus {}\nexport interface B {\n\tx: string;\n}\n';
		expect(withoutRoot(ts)).toBe('export type A = B;\nexport interface B {\n\tx: string;\n}\n');
		expect(withoutRoot('export interface B {}\n')).toBe('export interface B {}\n');
	});
});
