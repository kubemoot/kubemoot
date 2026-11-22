<script lang="ts">
	import { ACTIONS } from '#lib/action-definitions.js';
	import ActionMenu, { type MenuEntry } from '#lib/components/common/ActionMenu.svelte';
	import DeleteResourceDialog from '#lib/components/common/DeleteResourceDialog.svelte';
	import { CopyFlash } from '#lib/copy-flash.svelte.js';
	import { crewDisplayName } from '#lib/crew-display-name.js';
	import { crewGoes } from '#lib/crew-delete.js';
	import type { Crew } from '#lib/types/kubemoot.js';

	interface Props {
		crew: Crew;
		/** The address of the crew's page, offered as Open on the list; left out on the page itself. */
		openHref?: string;
		/** Delete is offered to a signed-in person only: it acts as them. */
		signedIn: boolean;
		/** False once the crew is already being deleted. */
		canDelete?: boolean;
		/** The crew is deleted; the page leaves it. */
		ondeleted: (crew: Crew) => void;
		/** Replaceable so a test can answer without a server. */
		fetchFn?: typeof fetch;
	}

	let { crew, openHref, signedIn, canDelete = true, ondeleted, fetchFn }: Props = $props();

	let asking = $state(false);
	const flash = new CopyFlash();

	const entries = $derived<MenuEntry[]>([
		...(openHref ? [{ kind: 'link' as const, label: 'Open', href: openHref }] : []),
		{ kind: 'item', label: 'Copy name', onselect: () => void flash.copy('name', `${crew.metadata.namespace}/${crew.metadata.name}`) },
		...(signedIn && canDelete ? [{ kind: 'separator' as const }, { kind: 'item' as const, label: 'Delete...', danger: true, onselect: () => (asking = true) }] : [])
	]);
</script>

{#if flash.copied}<span class="copied" role="status">Copied</span>{/if}
<ActionMenu {entries} label="Actions for {crewDisplayName(crew)}" tip={ACTIONS.rowActions} icon="more" />

{#if asking}
	<DeleteResourceDialog
		kind="crew"
		namespace={crew.metadata.namespace ?? ''}
		name={crew.metadata.name}
		question="Delete the crew {crewDisplayName(crew)}?"
		goes={crewGoes(crew)}
		onclose={() => (asking = false)}
		ondeleted={() => {
			asking = false;
			ondeleted(crew);
		}}
		{fetchFn}
	/>
{/if}

<style>
	.copied { margin-right: 0.4rem; font-size: 0.75rem; color: var(--color-success); align-self: center; }
</style>
