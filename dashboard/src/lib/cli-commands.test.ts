import { describe, expect, it } from 'vitest';
import {
	askCommand,
	deleteCrewCommand,
	deleteSuiteCommand,
	listConversationsCommand,
	memoryAddCommand,
	memoryClearCommand,
	memoryDeleteCommand,
	memoryEditCommand,
	memoryExportCommand,
	replayConversationCommand,
	reportUnpinCommand,
	reportVerdictCommand,
	shellQuote,
	suiteControlCommand
} from './cli-commands';

describe('shellQuote', () => {
	it('leaves a plain name as it is', () => {
		expect(shellQuote('homelab-pilot')).toBe('homelab-pilot');
		expect(shellQuote('crew-homelab-pilot')).toBe('crew-homelab-pilot');
	});

	it('quotes an empty word, spaces and shell characters', () => {
		expect(shellQuote('')).toBe("''");
		expect(shellQuote('a b')).toBe("'a b'");
		expect(shellQuote('a;rm -rf /')).toBe("'a;rm -rf /'");
		expect(shellQuote('$(id)')).toBe("'$(id)'");
	});

	it('escapes a single quote inside the word', () => {
		expect(shellQuote("it's")).toBe(String.raw`'it'\''s'`);
	});
});

describe('commands', () => {
	it('asks a crew, with an optional conversation and namespace', () => {
		expect(askCommand('demo', 'crew-demo')).toBe('kmctl conversation ask demo "your question" -n crew-demo');
		expect(askCommand('demo', null, 'abc')).toBe('kmctl conversation ask demo "your question" --conversation-id abc');
		expect(askCommand('demo')).toBe('kmctl conversation ask demo "your question"');
	});

	it('lists a crew conversations', () => {
		expect(listConversationsCommand('demo', 'crew-demo')).toBe('kmctl conversation list demo -n crew-demo');
	});

	it('removes a crew and a suite run', () => {
		expect(deleteCrewCommand('demo', 'crew-demo')).toBe('kmctl delete crew demo -n crew-demo');
		expect(deleteSuiteCommand('s1', 'ns')).toBe('kmctl delete crewfitnesssuite s1 -n ns');
	});

	it('controls a suite with kmctl fitness, one verb for each action', () => {
		expect(suiteControlCommand('pause', 's1', 'ns')).toBe('kmctl fitness pause s1 -n ns');
		expect(suiteControlCommand('resume', 's1')).toBe('kmctl fitness resume s1');
		expect(suiteControlCommand('stop', 's1', 'ns')).toBe('kmctl fitness stop s1 -n ns');
	});

	it('pins and unpins the verdict of a report with kmctl mcp report', () => {
		expect(reportVerdictCommand('r1', 'ns')).toBe('kmctl mcp report pin r1 --verdict use -n ns');
		expect(reportUnpinCommand('r1', 'ns')).toBe('kmctl mcp report unpin r1 -n ns');
	});

	it('replays a conversation by crew and id', () => {
		expect(replayConversationCommand('demo', 'abc123', 'crew-demo')).toBe('kmctl conversation replay demo abc123 -n crew-demo');
	});

	it('writes crew memory with kmctl memory', () => {
		expect(memoryAddCommand('demo', 'ns')).toBe('kmctl memory add demo TOPIC KEY "value" -n ns');
		expect(memoryEditCommand('demo', 'ns')).toBe('kmctl memory edit demo TOPIC KEY "new value" -n ns');
		expect(memoryDeleteCommand('demo', 'ns')).toBe('kmctl memory delete demo TOPIC KEY -n ns');
		expect(memoryClearCommand('demo', 'ns')).toBe('kmctl memory clear demo -n ns');
	});

	it('never offers a kubectl patch', () => {
		const all = [
			suiteControlCommand('stop', 's'),
			reportVerdictCommand('r'),
			reportUnpinCommand('r'),
			memoryAddCommand('c'),
			replayConversationCommand('c', 'i')
		];
		for (const command of all) expect(command).toMatch(/^kmctl /);
	});

	it('exports a crew memory', () => {
		expect(memoryExportCommand('demo', 'crew-demo')).toBe('kmctl memory export demo -n crew-demo');
	});

	it('quotes a name that is not a plain word, so the command cannot inject', () => {
		expect(deleteCrewCommand("a'b; rm -rf /", 'ns')).toBe(String.raw`kmctl delete crew 'a'\''b; rm -rf /' -n ns`);
	});
});
