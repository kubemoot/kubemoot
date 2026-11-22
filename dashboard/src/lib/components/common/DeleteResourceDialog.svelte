<script lang="ts">
	import ConfirmDialog from './ConfirmDialog.svelte';
	import { DeleteRefused, deleteResource, type DeleteKind } from '#lib/resource-delete-client.js';

	interface Props {
		kind: DeleteKind;
		namespace: string;
		name: string;
		/** What the person reads as the question, such as "Delete the fitness run gate-run?". */
		question: string;
		/** What goes with it, one line each; the operator's finalizer removes these. */
		goes: string[];
		/** The person changed their mind. */
		onclose: () => void;
		/** The resource is deleted; the page drops it. */
		ondeleted: () => void;
		/** Replaceable so a test can answer without a server. */
		fetchFn?: typeof fetch;
	}

	let { kind, namespace, name, question, goes, onclose, ondeleted, fetchFn }: Props = $props();

	let busy = $state(false);
	let refusal = $state<string | null>(null);

	async function confirm() {
		if (busy) return;
		busy = true;
		refusal = null;
		try {
			await deleteResource(fetchFn ?? fetch, kind, namespace, name);
			ondeleted();
		} catch (err) {
			refusal = err instanceof DeleteRefused ? err.message : 'The delete failed. Try again.';
		} finally {
			busy = false;
		}
	}
</script>

<ConfirmDialog
	{question}
	{goes}
	{refusal}
	{busy}
	onconfirm={() => void confirm()}
	{onclose}
/>
