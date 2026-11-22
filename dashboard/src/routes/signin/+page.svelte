<script lang="ts">
	import { PageTitle } from '#lib/components/common/index.js';
	import { enhance } from '$app/forms';
	import { resolve } from '$app/paths';
	import type { ActionData } from './$types';

	let { form }: { form: ActionData } = $props();
	let working = $state(false);
</script>

<svelte:head><title>Sign in - Kubemoot</title></svelte:head>

<div class="signin">
	<PageTitle route="/signin" />
	<p class="lead">
		Paste a Kubernetes bearer token. The dashboard checks it with the cluster and then acts as you: what you
		see, and which crews you may ask, is decided by your own Kubernetes permissions.
	</p>

	<form
		method="POST"
		use:enhance={() => {
			working = true;
			return async ({ update }) => {
				await update({ reset: false });
				working = false;
			};
		}}
	>
		<label for="token">Bearer token</label>
		<input id="token" name="token" type="password" autocomplete="off" spellcheck="false" required />
		{#if form?.message}
			<p class="error" role="alert">{form.message}</p>
		{/if}
		<button type="submit" disabled={working}>{working ? 'Checking...' : 'Sign in'}</button>
	</form>

	<details>
		<summary>How to get a token</summary>
		<p>With any kubectl, for a ServiceAccount you may create tokens for (twelve hours):</p>
		<pre>{'kubectl create token <serviceaccount> -n <namespace> --duration=12h'}</pre>
		<p>Or with kmctl, a token for your own account:</p>
		<pre>kmctl dashboard token</pre>
		<p class="muted">
			The token stays in the dashboard server's memory for this session and is never written to disk or sent to
			your browser's scripts. Signing out forgets it.
		</p>
	</details>
	<p class="muted"><a href={resolve('/')}>Back</a></p>
</div>

<style>
	.signin {
		max-width: 34rem;
		margin: 8vh auto;
		padding: 0 1.5rem;
	}
	.lead,
	.muted {
		color: var(--color-text-muted);
		line-height: 1.5;
		margin-bottom: 1rem;
	}
	form {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 1.5rem;
	}
	input {
		padding: 0.6rem 0.75rem;
		border-radius: 6px;
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text);
		font-family: var(--font-mono);
	}
	button {
		padding: 0.6rem 1rem;
		border: none;
		border-radius: 6px;
		background: var(--color-primary);
		color: white;
		font-weight: 600;
		align-self: flex-start;
	}
	button:disabled {
		opacity: 0.6;
		cursor: default;
	}
	.error {
		color: var(--color-error);
	}
	pre {
		background: var(--color-bg-secondary);
		padding: 0.5rem 0.75rem;
		border-radius: 6px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		margin: 0.5rem 0 1rem;
	}
	details {
		margin-bottom: 1rem;
	}
	summary {
		cursor: pointer;
		margin-bottom: 0.5rem;
	}
</style>
