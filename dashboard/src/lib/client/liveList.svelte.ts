import { resolve } from '$app/paths';

/** The CRD list endpoints under /api/kubemoot that a LiveList can read and watch. */
export type LivePlural =
	| 'agents'
	| 'crewfitnesssuites'
	| 'crews'
	| 'embeddingmodels'
	| 'mcpcatalogs'
	| 'mcpgateways'
	| 'mcpqualitypolicies'
	| 'mcpservers'
	| 'modelproviders'
	| 'models'
	| 'promptmodules'
	| 'ragsources';

interface K8sObj {
	metadata: { name: string; namespace?: string; creationTimestamp?: string };
}

/**
 * LiveList - a reusable push-based list for a kubemoot CRD page.
 *
 * Does one initial list fetch (fast first render), then attaches an EventSource
 * to /api/kubemoot/watch/{plural} and reconciles ADDED/MODIFIED/DELETED events
 * into a keyed list IN PLACE. Replaces per-page polling: realtime, flicker-free,
 * and near-zero idle server load (one watch per resource vs every client
 * polling on a timer). EventSource auto-reconnects, so it's self-healing.
 *
 * Usage in a +page.svelte:
 *   const live = new LiveList<Agent>('agents');
 *   onMount(() => { live.start($namespace); return () => live.stop(); });
 *   $effect(() => { live.setNamespace($namespace); });
 *   ...render live.items / live.loading / live.error
 */
export class LiveList<T extends K8sObj> {
	items = $state<T[]>([]);
	loading = $state(true);
	error = $state<string | null>(null);

	readonly #plural: LivePlural;
	#es: EventSource | null = null;
	#ns = '';
	#started = false;

	constructor(plural: LivePlural) {
		this.#plural = plural;
	}

	#key(o: K8sObj): string {
		return `${o.metadata.namespace ?? ''}/${o.metadata.name}`;
	}
	readonly #cmp = (a: T, b: T) =>
		(b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? '') ||
		a.metadata.name.localeCompare(b.metadata.name);

	async #fetchInitial(silent: boolean) {
		if (!silent) this.loading = true;
		try {
			const listPath = resolve(`/api/kubemoot/${this.#plural}`);
			const res = await fetch(`${listPath}?namespace=${this.#ns}`);
			const data = await res.json();
			this.items = ((data.items as T[]) || []).slice().sort(this.#cmp);
			this.error = null;
		} catch (e) {
			if (!silent) this.error = e instanceof Error ? e.message : 'Failed to load';
		} finally {
			if (!silent) this.loading = false;
		}
	}

	#connect() {
		this.#disconnect();
		this.#es = new EventSource(
			`${resolve('/api/kubemoot/watch/[plural]', { plural: this.#plural })}?namespace=${this.#ns}`
		);
		this.#es.onmessage = (e) => {
			let ev: { kind?: string; type?: string; object?: T };
			try {
				ev = JSON.parse(e.data);
			} catch {
				return;
			}
			if (!ev.kind || !ev.object) return; // 'synced' / heartbeat markers
			const obj = ev.object;
			const k = this.#key(obj);
			if (ev.type === 'DELETED') {
				this.items = this.items.filter((x) => this.#key(x) !== k);
			} else if (ev.type === 'ADDED' || ev.type === 'MODIFIED') {
				const i = this.items.findIndex((x) => this.#key(x) === k);
				if (i >= 0) {
					this.items[i] = obj;
					this.items = [...this.items];
				} else {
					this.items = [...this.items, obj].sort(this.#cmp);
				}
			}
		};
		// EventSource auto-reconnects on error; no manual retry needed.
	}

	#disconnect() {
		this.#es?.close();
		this.#es = null;
	}

	/** Initial load + attach the live watch. Call from onMount. */
	start(ns: string) {
		this.#ns = ns;
		this.#started = true;
		this.#fetchInitial(false).then(() => this.#connect());
	}

	/** Re-point at a new namespace (no-op if unchanged). Call from a $effect on $namespace. */
	setNamespace(ns: string) {
		if (!this.#started || ns === this.#ns) return;
		this.#ns = ns;
		this.#fetchInitial(false).then(() => this.#connect());
	}

	/** Silent re-fetch (no loading flash) - for immediate feedback after a
	 * card-initiated mutation; the watch would also deliver the change shortly. */
	refresh() {
		this.#fetchInitial(true);
	}

	/** Close the watch. Call from the onMount cleanup. */
	stop() {
		this.#disconnect();
	}
}
