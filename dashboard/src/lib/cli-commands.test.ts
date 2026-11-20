import { describe, expect, it } from 'vitest';
import {
	askCommand,
	deleteCrewCommand,
	deleteSuiteCommand,
	listConversationsCommand,
	memoryExportCommand,
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

	it('sets the suite control field for each action', () => {
		expect(suiteControlCommand('pause', 's1', 'ns')).toBe(
			`kubectl patch crewfitnesssuite s1 --type=merge -p '{"spec":{"suspend":true}}' -n ns`
		);
		expect(suiteControlCommand('resume', 's1')).toContain('"suspend":false');
		expect(suiteControlCommand('stop', 's1')).toContain('"cancel":true');
	});

	it('patches the admin verdict of a report', () => {
		expect(reportVerdictCommand('r1', 'ns')).toContain('kubectl patch mcpserverreport r1 --type=merge');
		expect(reportVerdictCommand('r1', 'ns')).toContain('"adminVerdict"');
	});

	it('exports a crew memory', () => {
		expect(memoryExportCommand('demo', 'crew-demo')).toBe('kmctl memory export demo -n crew-demo');
	});

	it('quotes a name that is not a plain word, so the command cannot inject', () => {
		expect(deleteCrewCommand("a'b; rm -rf /", 'ns')).toBe(String.raw`kmctl delete crew 'a'\''b; rm -rf /' -n ns`);
	});
});
