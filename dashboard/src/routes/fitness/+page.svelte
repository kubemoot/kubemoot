<script lang="ts">
	import { onMount } from 'svelte';
	import { marked } from 'marked';
	import { base } from '$app/paths';
	import { namespace, showStandAsides } from '$stores';
	import type { CrewFitnessSuite } from '$types/kubemoot.js';
	import { SYNTHESIS_COLLAPSE_CHARS, artifactKey, artifactHref } from '$lib/discussion-artifacts';

	// Synthesis/advisory are LLM markdown (headers, lists, bold). Render them as
	// markdown — matching the Discussions view — instead of raw text.
	marked.setOptions({ breaks: true, gfm: true });
	const md = (s: string | undefined) => marked.parse(s ?? '') as string;
	// Inline variant for one-line messages (agent finding summaries) — renders
	// bold/code/links without wrapping each in a block <p>.
	const mdInline = (s: string | undefined) => marked.parseInline(s ?? '') as string;

	// Per-thread "copy thread id" (multiple conversations can be open at once).
	let copiedTid = $state<string | null>(null);
	async function copyThreadId(threadId: string | undefined, question: string | undefined) {
		if (!threadId) return;
		await navigator.clipboard.writeText(`Thread id: ${threadId}${question ? ` | ${question}` : ''}`);
		copiedTid = threadId;
		setTimeout(() => { if (copiedTid === threadId) copiedTid = null; }, 2000);
	}

	// Copyable, scrollable error surface. Replaces native alert(): alert() truncates,
	// can't be resized, and can't be copied — which matters for long K8s API errors
	// (e.g. an RBAC 403 Status body) the user needs to share when asking for support.
	let errorBox = $state<string | null>(null);
	let copiedError = $state(false);
	async function copyError() {
		if (!errorBox) return;
		await navigator.clipboard.writeText(errorBox);
		copiedError = true;
		setTimeout(() => { copiedError = false; }, 2000);
	}

	interface Iteration {
		key: string;
		scriptIdx: number;
		iter: number;
		scenario: string;
		status: string;
		assertionsPassed: number;
		assertionsTotal: number;
		durationMs: number;
		running?: boolean; // synthetic row for an in-flight child (no transcript yet)
	}
	interface AssertionResult {
		raw: string;
		passed: boolean;
		message: string;
	}
	interface SignalEvent {
		type: string;
		agent?: string;
		status?: string;
		gpu?: string;
		signal?: string;
		summary?: string;
		content?: string;
		threadId?: string;
		stood_aside?: boolean;
		error?: string;
	}
	interface Transcript {
		assertions?: AssertionResult[];
		events?: SignalEvent[];
		conversationId?: string;
		threadId?: string;
		question?: string;
		startedAt?: string;
		durationMs?: number;
	}
	interface FitnessTest {
		metadata: { name: string; namespace: string; labels?: Record<string, string>; creationTimestamp?: string };
		spec?: { testRef?: string };
		status?: { phase?: string; durationMs?: number };
	}

	let suites = $state<CrewFitnessSuite[]>([]);
	let standaloneTests = $state<FitnessTest[]>([]);
	// Live in-flight suite children, keyed by suite id (ns/name). A suite child
	// has no transcript until it completes, so the iterations table can't show
	// what's running RIGHT NOW. We track the running CrewFitness child CRs from
	// the watch and surface a "Running now" line in the expanded suite.
	let running = $state<Record<string, FitnessTest[]>>({});
	let agentNames = $state<Set<string>>(new Set());

	const TERMINAL_PHASES = new Set(['Passed', 'Failed', 'Completed', 'Error']);

	function childSuiteId(t: FitnessTest): string | null {
		const suite = t.metadata.labels?.[SUITE_LABEL];
		if (!suite) return null;
		return `${t.metadata.namespace ?? ''}/${suite}`;
	}
	function childScenario(t: FitnessTest): string {
		return t.spec?.testRef ?? t.metadata.name.replace(/^run-[^-]+-/, '');
	}
	function childIter(t: FitnessTest): number | null {
		const m = /-i(\d+)$/.exec(t.metadata.name);
		return m ? Number(m[1]) : null;
	}
	// Upsert/remove a suite child into `running`, dropping it once terminal.
	function trackChild(t: FitnessTest, deleted = false) {
		const id = childSuiteId(t);
		if (!id) return;
		const terminal = deleted || TERMINAL_PHASES.has(t.status?.phase ?? '');
		const list = (running[id] ?? []).filter((x) => x.metadata.name !== t.metadata.name);
		if (!terminal) list.push(t);
		running = { ...running, [id]: list };
		// A child just finished — pull the updated iteration rows so per-scenario
		// counts advance live (the suite tally already updates from its own watch).
		if (terminal) scheduleIterRefetch(id);
	}
	let loading = $state(true);
	let error = $state<string | null>(null);

	let openSuite = $state<Record<string, boolean>>({});
	let iterations = $state<Record<string, Iteration[]>>({});
	let iterLoading = $state<Record<string, boolean>>({});
	let openScenario = $state<Record<string, boolean>>({});
	let openIter = $state<Record<string, boolean>>({});
	// Per-finding expand toggle: agree findings show the first-line summary by
	// default and expand to the full captured content on demand.
	let openFinding = $state<Record<string, boolean>>({});
	// Per-synthesis expand toggle: a long synthesis collapses to a faded preview
	// (max-height) by default and expands to the full text on demand. The marker
	// parse + download URL + collapse threshold are shared with the live discussions
	// view via $lib/discussion-artifacts so the two stay in lockstep.
	let openSynthesis = $state<Record<string, boolean>>({});
	let transcripts = $state<Record<string, Transcript | { error: string }>>({});
	// Per-suite DEFER/REFLECTS quality scores { suiteId: { scenario: score } }, loaded
	// alongside the iteration list. The deferred judge writes these post-suite, so they
	// are empty until that pass runs (the DEFER line shows "pending judge" until then).
	let scores = $state<Record<string, Record<string, number>>>({});
	// Per-suite DEFER/REFLECTS judge rationale { suiteId: { scenario: reason } }, shown
	// on the DEFER assert row so the score's "why" is visible without transcript digging.
	let reasons = $state<Record<string, Record<string, string>>>({});
	// REFLECTS scores may be stored 0-1 or 0-100; normalize to a 0-100 integer for display.
	function fmtScore(v: number): number { return v <= 1 ? Math.round(v * 100) : Math.round(v); }
	// Quality band for a 0-100 REFLECTS score: drives the colour of the score pill so a
	// low score reads as a failure (red), not a pass. Matches the DEFER assert-row bands.
	function scoreBand(n: number): string {
		if (n >= 70) return 'ok';
		if (n >= 40) return 'mid';
		return 'bad';
	}

	// Post-suite DEFER/REFLECTS judging progress per suite: { complete, judged }. The
	// judge pass runs AFTER the suite reaches a terminal phase and scores one scenario
	// at a time (resumable checkpoint), so a "Completed" suite can still be judging for
	// a while. Polled live (judgePollLoop) so the suite badge and per-scenario state
	// advance without a manual refresh.
	let judgeState = $state<Record<string, { complete: boolean; judged: number }>>({});
	// True when the suite has finished running but its quality judging is still going.
	function isJudging(id: string, phase?: string): boolean {
		return TERMINAL_PHASES.has(phase ?? '') && !!judgeState[id] && !judgeState[id].complete;
	}

	interface ScenarioGroup {
		scenario: string;
		scriptIdx: number;
		iterations: Iteration[];
		passed: number;
		total: number; // completed (terminal) iterations only
		meanMs: number;
		running: boolean; // a child for this scenario is in flight
	}

	// Merge the completed (transcript) iterations with a synthetic row for each
	// in-flight child, so the spinner lands on the row that will become the
	// result. Deduped by scenario+iter — once the transcript arrives it replaces
	// the synthetic running row.
	function mergedIterations(id: string): Iteration[] {
		const done = iterations[id] ?? [];
		const seen = new Set(done.map((i) => `${i.scenario}#${i.iter}`));
		const live: Iteration[] = (running[id] ?? []).map((rc) => {
			const m = /-s(\d+)-i(\d+)$/.exec(rc.metadata.name);
			return {
				key: `running:${rc.metadata.name}`,
				scriptIdx: m ? Number(m[1]) : 999,
				iter: childIter(rc) ?? 0,
				scenario: childScenario(rc),
				status: 'Running',
				assertionsPassed: 0,
				assertionsTotal: 0,
				durationMs: 0,
				running: true
			};
		}).filter((it) => !seen.has(`${it.scenario}#${it.iter}`));
		return [...done, ...live];
	}

	// Group a suite's flat iteration list by scenario, with per-scenario rollups
	// (pass count + mean duration) so each scenario shows once instead of N rows.
	function groupScenarios(its: Iteration[]): ScenarioGroup[] {
		const m = new Map<string, Iteration[]>();
		for (const it of its) {
			if (!m.has(it.scenario)) m.set(it.scenario, []);
			m.get(it.scenario)!.push(it);
		}
		const groups: ScenarioGroup[] = [];
		for (const [scenario, list] of m) {
			const completed = list.filter((i) => !i.running);
			const passed = completed.filter((i) => i.status === 'Passed').length;
			const durs = completed.map((i) => i.durationMs).filter((d) => d > 0);
			const meanMs = durs.length ? Math.round(durs.reduce((a, b) => a + b, 0) / durs.length) : 0;
			groups.push({
				scenario,
				scriptIdx: list[0].scriptIdx,
				iterations: [...list].sort((a, b) => a.iter - b.iter),
				passed,
				total: list.length,
				meanMs,
				running: list.some((i) => i.running)
			});
		}
		return groups.sort((a, b) => a.scriptIdx - b.scriptIdx);
	}

	const SUITE_LABEL = 'kubemoot.ai/fitness-suite';

	let initialized = $state(false);
	let lastNs = $state<string | null>(null);

	// silent=true: a background refresh-tick. Do NOT toggle `loading` (which would
	// blank the table behind the "Loading…" placeholder = flicker). Just swap the
	// data; the keyed {#each} blocks below diff rows in place. Only the initial
	// load and a namespace switch show the loading state.
	async function fetchAll(silent = false) {
		if (!silent) loading = true;
		error = null;
		try {
			const [sRes, tRes, aRes] = await Promise.all([
				fetch(`${base}/api/kubemoot/crewfitnesssuites?namespace=${$namespace}`),
				fetch(`${base}/api/kubemoot/crewfitnesses?namespace=${$namespace}`),
				fetch(`${base}/api/kubemoot/agents?namespace=${$namespace}`)
			]);
			const sData = await sRes.json();
			const tData = await tRes.json();
			const aData = await aRes.json();
			suites = (sData.items || []).sort((a: CrewFitnessSuite, b: CrewFitnessSuite) =>
				(b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? '')
			);
			const allTests: FitnessTest[] = tData.items || [];
			standaloneTests = allTests.filter((t) => !t.metadata.labels?.[SUITE_LABEL]);
			// Seed "Running now" from the initial list so it shows before the first
			// watch event; the watch keeps it current thereafter.
			const nextRunning: Record<string, FitnessTest[]> = {};
			for (const t of allTests) {
				const id = childSuiteId(t);
				if (!id || TERMINAL_PHASES.has(t.status?.phase ?? '')) continue;
				nextRunning[id] ??= [];
				nextRunning[id].push(t);
			}
			running = nextRunning;
			// Names of agents currently deployed — only these get a link from the
			// conversation (the agent's version must exist in the crew to look it up).
			agentNames = new Set(
				(aData.items || []).map((a: { metadata?: { name?: string } }) => a.metadata?.name).filter(Boolean)
			);
		} catch (e) {
			if (!silent) error = e instanceof Error ? e.message : 'Failed to load fitness data';
		} finally {
			if (!silent) loading = false;
			initialized = true;
		}
	}

	// --- Live updates via watch→SSE (no polling) ---------------------------
	// The server opens a k8s watch and pushes add/update/delete events; we
	// reconcile them into the keyed lists in place. EventSource auto-reconnects
	// on drop, so the page is self-healing without a poll loop.
	let es: EventSource | null = null;

	const byCreatedDesc = (a: CrewFitnessSuite, b: CrewFitnessSuite) =>
		(b.metadata.creationTimestamp ?? '').localeCompare(a.metadata.creationTimestamp ?? '');

	function upsertSuite(obj: CrewFitnessSuite) {
		const name = obj?.metadata?.name;
		if (!name) return;
		const i = suites.findIndex((s) => s.metadata.name === name);
		if (i >= 0) {
			suites[i] = obj;
			suites = [...suites];
		} else {
			suites = [...suites, obj].sort(byCreatedDesc);
		}
	}
	function removeSuite(obj: CrewFitnessSuite) {
		suites = suites.filter((s) => s.metadata.name !== obj?.metadata?.name);
	}
	function upsertTest(obj: FitnessTest) {
		if (obj?.metadata?.labels?.[SUITE_LABEL]) {
			trackChild(obj); // suite child — feeds the "Running now" line, not the standalone list
			return;
		}
		const name = obj?.metadata?.name;
		if (!name) return;
		const i = standaloneTests.findIndex((t) => t.metadata.name === name);
		if (i >= 0) {
			standaloneTests[i] = obj;
			standaloneTests = [...standaloneTests];
		} else {
			standaloneTests = [...standaloneTests, obj];
		}
	}
	function removeTest(obj: FitnessTest) {
		if (obj?.metadata?.labels?.[SUITE_LABEL]) {
			trackChild(obj, true); // suite child deleted — drop from "Running now"
			return;
		}
		standaloneTests = standaloneTests.filter((t) => t.metadata.name !== obj?.metadata?.name);
	}

	function closeWatch() {
		es?.close();
		es = null;
	}
	function connectWatch() {
		closeWatch();
		es = new EventSource(`${base}/api/kubemoot/fitness/watch?namespace=${$namespace}`);
		es.onmessage = (e) => {
			try {
				const ev = JSON.parse(e.data);
				if (!ev.kind) return; // 'synced'/heartbeat markers
				if (ev.kind === 'CrewFitnessSuite') {
					if (ev.type === 'DELETED') removeSuite(ev.object);
					else if (ev.type === 'ADDED' || ev.type === 'MODIFIED') upsertSuite(ev.object);
				} else if (ev.kind === 'CrewFitness') {
					if (ev.type === 'DELETED') removeTest(ev.object);
					else if (ev.type === 'ADDED' || ev.type === 'MODIFIED') upsertTest(ev.object);
				}
			} catch {
				/* ignore malformed frames */
			}
		};
		// EventSource reconnects automatically on error — no manual retry needed.
	}

	// Poll DEFER judging progress for terminal-but-not-complete suites so the
	// "judging" badge and per-scenario state advance live (the suite-status watch
	// stops emitting once a suite is terminal, but judging continues after that).
	async function refreshJudging() {
		for (const suite of suites) {
			const ns = suite.metadata.namespace ?? '';
			const name = suite.metadata.name ?? '';
			const id = `${ns}/${name}`;
			if (!TERMINAL_PHASES.has(suite.status?.phase ?? '')) continue;
			if (judgeState[id]?.complete) continue; // judging finished — stop polling this one
			try {
				const r = await fetch(`${base}/api/kubemoot/crewfitnesssuites/${ns}/${name}/scores`);
				if (!r.ok) continue;
				const sj = await r.json();
				scores[id] = sj.scores || {};
				reasons[id] = sj.reasons || {};
				judgeState[id] = { complete: !!sj.complete, judged: sj.judged ?? 0 };
			} catch {
				/* transient — retry next tick */
			}
		}
	}

	// Remove a suite run: delete the CrewFitnessSuite CR (children GC via owner
	// refs; the operator finalizer purges the run's NATS artifacts). Optimistically
	// drop it from the list; the live watch confirms the removal.
	async function deleteSuite(ns: string, name: string, e: Event) {
		e.stopPropagation();
		if (!confirm(`Remove suite run "${name}"?\n\nThis deletes the run and its stored artifacts (transcripts + scores) and cannot be undone.`)) {
			return;
		}
		try {
			const r = await fetch(`${base}/api/kubemoot/crewfitnesssuites/${ns}/${name}`, { method: 'DELETE' });
			if (!r.ok) {
				const d = await r.json().catch(() => ({}));
				const detail = d.error ?? d.message ?? JSON.stringify(d, null, 2);
				errorBox = `Delete failed (HTTP ${r.status})\n\nSuite: ${ns}/${name}\n\n${detail}`;
				return;
			}
			suites = suites.filter((s) => !(s.metadata.namespace === ns && s.metadata.name === name));
		} catch (err) {
			errorBox = `Delete failed\n\nSuite: ${ns}/${name}\n\n${err instanceof Error ? (err.stack ?? err.message) : String(err)}`;
		}
	}

	onMount(() => {
		// Initial list for fast first render, then attach the live watch.
		fetchAll(false).then(connectWatch).then(refreshJudging);
		const judgeTimer = setInterval(refreshJudging, 10_000);
		return () => { clearInterval(judgeTimer); closeWatch(); };
	});
	$effect(() => {
		const ns = $namespace;
		if (!initialized) return; // onMount does the first load + watch
		if (ns !== lastNs) {
			lastNs = ns;
			// Namespace switch: visible reload, then re-point the watch.
			fetchAll(false).then(connectWatch);
		}
	});

	// Fetch a suite's completed-iteration list (transcript-backed). silent=true
	// is a live refresh (no loading flash) when a child completes — keyed {#each}
	// reconciles rows in place so counts tick up without a page refresh.
	async function loadIterations(ns: string, name: string, silent = false) {
		const id = `${ns}/${name}`;
		if (!silent) iterLoading[id] = true;
		try {
			const res = await fetch(`${base}/api/kubemoot/crewfitnesssuites/${ns}/${name}/iterations`);
			const data = await res.json();
			iterations[id] = data.iterations || [];
			try {
				const sres = await fetch(`${base}/api/kubemoot/crewfitnesssuites/${ns}/${name}/scores`);
				if (sres.ok) {
					const sj = await sres.json();
					scores[id] = sj.scores || {};
					reasons[id] = sj.reasons || {};
					judgeState[id] = { complete: !!sj.complete, judged: sj.judged ?? 0 };
				}
			} catch { /* REFLECTS scores are optional UI enrichment */ }
		} catch {
			if (!silent) iterations[id] = [];
		} finally {
			if (!silent) iterLoading[id] = false;
		}
	}

	// Debounced live refresh of an open suite's iteration rows. Triggered from the
	// watch when a child reaches a terminal phase: the suite tally updates from the
	// suite-status watch, but the per-scenario rows come from the transcript list,
	// which only this refresh advances (12/12 → 13/13) without a manual reload.
	const refetchTimers: Record<string, ReturnType<typeof setTimeout>> = {};
	function scheduleIterRefetch(id: string) {
		if (!iterations[id]) return; // suite not open/loaded — nothing displayed to refresh
		clearTimeout(refetchTimers[id]);
		refetchTimers[id] = setTimeout(() => {
			const slash = id.indexOf('/');
			loadIterations(id.slice(0, slash), id.slice(slash + 1), true);
		}, 1500);
	}

	async function toggleSuite(ns: string, name: string) {
		const id = `${ns}/${name}`;
		openSuite[id] = !openSuite[id];
		if (openSuite[id] && !iterations[id]) {
			await loadIterations(ns, name, false);
		}
	}

	async function toggleIter(ns: string, name: string, it: Iteration) {
		const tid = it.key;
		openIter[tid] = !openIter[tid];
		if (openIter[tid] && !transcripts[tid]) {
			try {
				const res = await fetch(
					`${base}/api/kubemoot/crewfitnesssuites/${ns}/${name}/transcript?key=${encodeURIComponent(it.key)}`
				);
				const data = await res.json();
				transcripts[tid] = res.ok ? data : { error: data.error || 'failed to load transcript' };
			} catch {
				transcripts[tid] = { error: 'fetch failed' };
			}
		}
	}

	// Older transcripts (captured before the gateway SSE dedup) recorded the
	// synthesis twice — the coordinator dual-published it and the gateway relayed
	// both copies. Collapse identical synthesis events at render so historical
	// runs read cleanly too.
	function dedupeSynthesis(events: SignalEvent[]): SignalEvent[] {
		const seen = new Set<string>();
		return events.filter((e) => {
			if (e.type !== 'synthesis') return true;
			const key = e.content ?? '';
			if (seen.has(key)) return false;
			seen.add(key);
			return true;
		});
	}

	function isTranscript(t: Transcript | { error: string } | undefined): t is Transcript {
		return !!t && !('error' in t);
	}

	function phaseClass(phase?: string): string {
		switch (phase) {
			case 'Completed':
			case 'Passed':
				return 'phase-pass';
			case 'Failed':
			case 'Error':
				return 'phase-fail';
			case 'Running':
				return 'phase-running';
			default:
				return 'phase-pending';
		}
	}

	// Elapsed ms for a suite: completed-minus-started, or now-minus-started while
	// still running. Undefined when there is no start time.
	function suiteDurMs(s?: CrewFitnessSuite['status']): number | undefined {
		if (!s?.startedAt) return undefined;
		const end = s.completedAt ? new Date(s.completedAt).getTime() : Date.now();
		return end - new Date(s.startedAt).getTime();
	}

	function fmtDur(ms?: number): string {
		if (!ms) return '—';
		if (ms < 1000) return `${ms}ms`;
		if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
		return `${Math.floor(ms / 60_000)}m ${Math.floor((ms % 60_000) / 1000)}s`;
	}

	function fmtDateTime(iso?: string): string {
		if (!iso) return '—';
		const d = new Date(iso);
		return isNaN(d.getTime()) ? '—' : d.toLocaleString();
	}

	// Tooltip for the Duration cell: the absolute start/end date-times behind the
	// relative duration (a running suite has no end yet).
	function durTitle(startedAt?: string, completedAt?: string): string {
		if (!startedAt) return 'No start time recorded';
		return `Started: ${fmtDateTime(startedAt)}\nEnded: ${completedAt ? fmtDateTime(completedAt) : 'running'}`;
	}

	function formatBytes(n?: number): string {
		if (!n) return '—';
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / 1024 / 1024).toFixed(2)} MB`;
	}

	// Copy a suite's run context to the clipboard as markdown, so it can be pasted
	// into a report or ticket without screenshotting or downloading the XLSX.
	let copiedKey = $state<string | null>(null);
	async function copySuite(suite: CrewFitnessSuite, e: Event) {
		e.stopPropagation(); // don't toggle the row open
		const s = suite.status;
		const ns = suite.metadata.namespace ?? '';
		const name = suite.metadata.name ?? '';
		const key = `${ns}/${name}`;
		const jp = judgeState[key];
		const scen = suite.spec.scripts?.map((x) => x.testRef) ?? [];
		let judgeLine = '';
		if (jp) judgeLine = `- Judge: ${jp.complete ? 'complete' : `judging ${jp.judged}/${scen.length}`}`;
		const text = [
			`# Fitness Suite: ${name}`,
			`- Crew: ${suite.spec.crewRef}`,
			`- Namespace: ${ns}`,
			suite.spec.description ? `- Description: ${suite.spec.description}` : '',
			`- Phase: ${s?.phase ?? 'Unknown'}`,
			`- Iterations: ${s?.iterationsCompleted ?? 0} / ${s?.iterationsTotal ?? 0}`,
			`- Results: ${s?.passed ?? 0} passed / ${s?.failed ?? 0} failed / ${s?.errored ?? 0} errored`,
			`- Duration: ${fmtDur(suiteDurMs(s)).replace('\u2014', '-')}`,
			`- Scenarios (${scen.length}): ${scen.join(', ')}`,
			judgeLine,
			s?.runId ? `- Run ID: ${s.runId}` : '',
			suite.metadata.creationTimestamp ? `- Created: ${suite.metadata.creationTimestamp}` : '',
			s?.artifactRef?.objectKey
				? `- Artifact: ${s.artifactRef.objectKey} (${formatBytes(s.artifactRef.sizeBytes)})`
				: ''
		]
			.filter(Boolean)
			.join('\n');
		try {
			await navigator.clipboard.writeText(text);
			copiedKey = key;
			setTimeout(() => {
				if (copiedKey === key) copiedKey = null;
			}, 1500);
		} catch {
			/* clipboard API unavailable (e.g. insecure context) */
		}
	}
</script>

<div class="page-header">
	<h1>Fitness</h1>
	<span class="count">{suites.length} suites · {standaloneTests.length} tests</span>
</div>

{#snippet agentTag(agent: string | undefined, ns: string)}
	{#if agent && agentNames.has(agent)}
		<a class="evtag agent-link" href="{base}/agents/{agent}?namespace={ns}" title="Open {agent} (deployed in this crew)" onclick={(e) => e.stopPropagation()}>{agent}</a>
	{:else}
		<span class="evtag" title={agent ? `${agent} is not in the current crew deployment` : ''}>{agent ?? ''}</span>
	{/if}
{/snippet}

{#snippet statusBadge(phase: string | undefined)}
	{#if phase === 'Running'}
		<span class="badge phase-running running-badge" title="Running"><span class="spin"></span>Running</span>
	{:else}
		<span class="badge {phaseClass(phase)}">{phase ?? '—'}</span>
	{/if}
{/snippet}

{#if loading}
	<p class="status-msg">Loading…</p>
{:else if error}
	<p class="status-msg error">{error}</p>
{:else if suites.length === 0 && standaloneTests.length === 0}
	<p class="status-msg">No fitness suites or tests in <code>{$namespace || 'all namespaces'}</code>.</p>
{:else}
	<table class="ftable">
		<thead>
			<tr>
				<th class="c-caret"></th>
				<th>Status</th>
				<th>Suite</th>
				<th>Pass / Fail / Err</th>
				<th>Progress</th>
				<th>Duration</th>
				<th>Report</th>
			</tr>
		</thead>
		{#each suites as suite (suite.metadata.uid ?? suite.metadata.name)}
			{@const s = suite.status}
			{@const ns = suite.metadata.namespace ?? ''}
			{@const name = suite.metadata.name ?? ''}
			{@const id = `${ns}/${name}`}
			<tbody>
				<tr class="suite-row" onclick={() => toggleSuite(ns, name)}>
					<td class="c-caret">{openSuite[id] ? '▼' : '▶'}</td>
					<td>
						{#if isJudging(id, s?.phase)}
							<span class="badge phase-running running-badge" title="Suite finished — quality judging in progress"><span class="spin"></span>judging {judgeState[id].judged}</span>
						{:else}
							{@render statusBadge(s?.phase)}
						{/if}
					</td>
					<td class="name">
						{name}
						{#if suite.spec.description}<div class="desc">{suite.spec.description}</div>{/if}
					</td>
					<td class="mono"><span class="passed">{s?.passed ?? 0}</span>/<span class="failed">{s?.failed ?? 0}</span>/<span class="errored">{s?.errored ?? 0}</span></td>
					<td class="mono">{s?.iterationsCompleted ?? 0}/{s?.iterationsTotal ?? 0}</td>
					<td class="mono" title={durTitle(s?.startedAt, s?.completedAt)}>{fmtDur(suiteDurMs(s))}</td>
					<td>
						<button class="copy-btn" title="Copy this suite's details to the clipboard" onclick={(e) => copySuite(suite, e)}>{copiedKey === id ? '✓' : '📋'}</button>
						{#if s?.artifactRef?.objectKey}
							<a class="dl" href="{base}/api/kubemoot/crewfitnesssuites/{ns}/{name}/artifact" download onclick={(e) => e.stopPropagation()} title="Download {formatBytes(s.artifactRef.sizeBytes)}">⬇ XLSX</a>
						{:else}<span class="muted small">—</span>{/if}
						<button class="del-btn" title="Remove this suite run (deletes the run and its artifacts)" aria-label="Remove suite run" onclick={(e) => deleteSuite(ns, name, e)}>✕</button>
					</td>
				</tr>

				{#if openSuite[id]}
					{@const merged = mergedIterations(id)}
					<tr class="sub">
						<td></td>
						<td colspan="6" class="sub-cell">
							{#if iterLoading[id]}
								<p class="status-msg small">Loading iterations…</p>
							{:else if merged.length === 0}
								<p class="status-msg small">No transcripts (older run predates capture, or TTL-pruned).</p>
							{:else}
								<table class="itable">
									<thead>
										<tr>
											<th class="c-caret"></th>
											<th>Scenario</th>
											<th>Passed</th>
											<th>Mean</th>
										</tr>
									</thead>
									{#each groupScenarios(merged) as g (g.scenario)}
										{@const sk = `${id}::${g.scenario}`}
										<tbody>
											<tr class="scenario-row" onclick={() => (openScenario[sk] = !openScenario[sk])}>
												<td class="c-caret">{openScenario[sk] ? '▼' : '▶'}</td>
												<td class="mono scenario">
													{g.scenario}
													{#if g.running}
														<span class="badge phase-running running-badge" title="Scenario running"><span class="spin"></span>running</span>
													{:else if scores[id]?.[g.scenario] !== undefined}
														{@const n = fmtScore(scores[id][g.scenario])}
														<span class="score-pill {scoreBand(n)}" title="REFLECTS quality {n}/100 (the scenario verdict)">{n}</span>
													{:else if isJudging(id, s?.phase)}
														<span class="badge phase-running running-badge" title="Quality judging in progress"><span class="spin"></span>judging</span>
													{/if}
												</td>
												<td>
													{#if g.running}
														<span class="badge phase-running running-badge"><span class="spin"></span>{g.total > 0 ? `${g.passed}/${g.total}` : 'running'}</span>
													{:else}
														<span class="badge {g.passed === g.total ? 'phase-neutral' : 'phase-fail'}" title="Iterations whose hard assertions passed (plumbing: ran, non-empty synthesis). Answer quality is the score, not this.">{g.passed}/{g.total}</span>
													{/if}
												</td>
												<td class="mono">{fmtDur(g.meanMs)}</td>
											</tr>
											{#if openScenario[sk]}
												<tr class="sub">
													<td></td>
													<td colspan="3" class="sub-cell">
														<table class="itable">
															<thead>
																<tr>
																	<th class="c-caret"></th>
																	<th>Iter</th>
																	<th>Status</th>
																	<th>Asserts</th>
																	<th>Duration</th>
																</tr>
															</thead>
															{#each g.iterations as it (it.key)}
																{@const tid = it.key}
																<tbody>
																	{#if it.running}<tr class="iter-row running"><td class="c-caret"></td><td class="mono">{it.iter}</td><td>{@render statusBadge('Running')}</td><td class="mono muted">—</td><td class="mono muted">—</td></tr>{:else}<tr class="iter-row" onclick={() => toggleIter(ns, name, it)}>
																		<td class="c-caret">{openIter[tid] ? '▼' : '▶'}</td>
																		<td class="mono">{it.iter}</td>
																		<td>{#if it.status === 'Passed'}<span class="badge phase-neutral" title="Discussion ran and hard assertions passed (plumbing). Answer quality is the REFLECTS score on the scenario row, not this.">ran</span>{:else}{@render statusBadge(it.status)}{/if}</td>
																		<td class="mono">{it.assertionsPassed}/{it.assertionsTotal}</td>
																		<td class="mono">{fmtDur(it.durationMs)}</td>
																	</tr>
																	{#if openIter[tid]}
																		<tr class="conv-row">
																			<td></td>
																			<td colspan="4" class="conv-cell">
																				{#if !transcripts[tid]}
																					<p class="status-msg small">Loading conversation…</p>
																				{:else if !isTranscript(transcripts[tid])}
																					<p class="status-msg small error">{(transcripts[tid] as { error: string }).error}</p>
																				{:else}
																					{@const t = transcripts[tid] as Transcript}
									{@const standAsides = (t.events ?? []).filter((e) => e.type === 'phase' && e.stood_aside)}
									{@const convEvents = dedupeSynthesis(t.events ?? [])}
																					{#if t.question}<div class="q"><strong>Q:</strong> {t.question}</div>{/if}
																					<div class="meta muted small">thread {t.threadId || '—'}{#if t.threadId}<button class="copy-tid" title="Copy thread id" onclick={(e) => { e.stopPropagation(); copyThreadId(t.threadId, t.question); }}>{copiedTid === t.threadId ? '✓' : '📋'}</button>{/if} · {fmtDur(t.durationMs)}</div>
																					{#if t.assertions && t.assertions.length > 0}
																						<div class="asserts">
																							{#each t.assertions as a}
																								{#if a.raw.startsWith('DEFER')}
																									{@const sv = scores[id]?.[g.scenario]}
																									{@const n = sv == null ? null : fmtScore(sv)}
																									{@const reason = reasons[id]?.[g.scenario]}
																									<div class="assert defer {n == null ? '' : scoreBand(n)}">
																										<span class="mark score">{n == null ? '⌛' : n}</span>
																										<span class="mono">{a.raw}</span>
																										<span class="amsg">{n == null ? '— pending judge' : `— REFLECTS quality ${n}/100`}</span>
																									</div>
																									{#if reason}<div class="judge-reason" title="Judge rationale for this score">↳ {reason}</div>{/if}
																								{:else}
																									<div class="assert {a.passed ? 'ok' : 'bad'}">
																										<span class="mark">{a.passed ? '✓' : '✗'}</span>
																										<span class="mono">{a.raw}</span>
																										{#if !a.passed && a.message}<span class="amsg">— {a.message}</span>{/if}
																									</div>
																								{/if}
																							{/each}
																						</div>
																					{/if}
																					<div class="convo">
																						{#each convEvents as ev, evIdx}
																							{#if ev.type === 'advisory'}
																								<div class="ev advisory"><span class="evtag">advisory</span><div class="md">{@html md(ev.content)}</div></div>
																							{:else if ev.type === 'phase'}
																								{#if !(ev.stood_aside && !$showStandAsides)}<div class="ev phase">{@render agentTag(ev.agent, ns)} {ev.stood_aside ? 'stood aside' : (ev.status ?? 'evaluating')}{ev.gpu ? ` · ${ev.gpu}` : ''}</div>{/if}
																							{:else if ev.type === 'finding'}
																								{@const fkey = `${tid}:${evIdx}`}
																								{@const full = ev.content || ev.summary || ''}
																								{@const short = ev.summary || ev.content || ''}
																								{@const hasMore = full.length > short.length}
																								{@const fArtifact = artifactKey(full)}<div class="ev finding">{@render agentTag(ev.agent, ns)} <span class="sig sig-{ev.signal}">{ev.signal}</span>{#if openFinding[fkey] || !hasMore}<div class="md">{@html md(full)}</div>{#if hasMore}<button class="finding-toggle" onclick={() => (openFinding[fkey] = false)}>show less</button>{/if}{:else}<span class="md-inline">{@html mdInline(short)}</span><button class="finding-toggle" onclick={() => (openFinding[fkey] = true)}>… more</button>{/if}{#if fArtifact}<a class="artifact-dl" href={artifactHref(fArtifact)} download title="Download the full spilled artifact ({fArtifact})">download</a>{/if}</div>
																							{:else if ev.type === 'synthesis'}
																								{@const skey = `${tid}:${evIdx}`}{@const synthLong = (ev.content?.length ?? 0) > SYNTHESIS_COLLAPSE_CHARS}{@const sArtifact = artifactKey(ev.content)}<div class="ev synthesis"><span class="evtag">synthesis</span><div class="md synth-body" class:collapsed={synthLong && !openSynthesis[skey]}>{@html md(ev.content)}</div>{#if synthLong}<button class="finding-toggle" onclick={() => (openSynthesis[skey] = !openSynthesis[skey])}>{openSynthesis[skey] ? 'show less' : '… more'}</button>{/if}{#if sArtifact}<a class="artifact-dl" href={artifactHref(sArtifact)} download title="Download the full spilled artifact ({sArtifact})">download</a>{/if}</div>
																							{:else if ev.type === 'error' || ev.error}
																								<div class="ev err"><span class="evtag">error</span> {ev.error ?? ev.content}</div>
																							{/if}
																						{/each}
																						{#if !$showStandAsides && standAsides.length > 0}
											<div class="ev standaside-note muted small">{standAsides.length} agent{standAsides.length === 1 ? '' : 's'} stood aside (hidden)</div>
										{/if}
										{#if (t.events ?? []).length === 0}<p class="status-msg small">No events captured.</p>{/if}
																					</div>
																				{/if}
																			</td>
																		</tr>
																	{/if}
																{/if}
																</tbody>
															{/each}
														</table>
													</td>
												</tr>
											{/if}
										</tbody>
									{/each}
								</table>
							{/if}
						</td>
					</tr>
				{/if}
			</tbody>
		{/each}
	</table>

	{#if standaloneTests.length > 0}
		<h2 class="section">Standalone tests</h2>
		<table class="ftable">
			<thead><tr><th>Status</th><th>Name</th><th>Test</th><th>Duration</th></tr></thead>
			<tbody>
				{#each standaloneTests as t (t.metadata.namespace + '/' + t.metadata.name)}
					<tr>
						<td><span class="badge {phaseClass(t.status?.phase)}">{t.status?.phase ?? '—'}</span></td>
						<td class="name">{t.metadata.name}</td>
						<td class="mono muted small">{t.spec?.testRef ?? ''}</td>
						<td class="mono">{fmtDur(t.status?.durationMs)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
{/if}

{#if errorBox}
	<div class="err-overlay" role="dialog" aria-modal="true" onclick={() => (errorBox = null)}>
		<div class="err-modal" onclick={(e) => e.stopPropagation()}>
			<div class="err-head">
				<span class="err-title">Error</span>
				<div class="err-actions">
					<button class="err-btn" onclick={copyError}>{copiedError ? '✓ Copied' : '⧉ Copy'}</button>
					<button class="err-btn" onclick={() => (errorBox = null)}>✕ Close</button>
				</div>
			</div>
			<pre class="err-body">{errorBox}</pre>
		</div>
	</div>
{/if}

<style>
	.page-header { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.4rem; }
	.page-header h1 { font-size: 1.5rem; font-weight: 600; }
	.count { background: var(--color-bg-tertiary); padding: 0.15rem 0.6rem; border-radius: 999px; font-size: 0.8rem; color: var(--color-text-muted); }
	.section { font-size: 0.95rem; font-weight: 600; color: var(--color-text-muted); margin: 1.5rem 0 0.5rem; }

	.status-msg { color: var(--color-text-muted); padding: 1rem; text-align: center; }
	.status-msg.error { color: var(--color-error); }
	.status-msg.small { padding: 0.5rem; font-size: 0.85rem; text-align: left; }
	.status-msg code { color: var(--color-cyan); font-family: var(--font-mono); }

	.ftable, .itable { width: 100%; border-collapse: collapse; }
	.ftable th, .ftable td { text-align: left; padding: 0.45rem 0.6rem; border-bottom: 1px solid var(--color-border); vertical-align: top; }
	.ftable th { font-weight: 600; color: var(--color-text-muted); font-size: 0.8rem; }
	.c-caret { width: 1.4rem; color: var(--color-text-muted); }
	.suite-row { cursor: pointer; }
	.suite-row:hover { background: var(--color-bg-tertiary); }
	.name { font-weight: 600; }
	.desc { font-weight: 400; font-size: 0.75rem; color: var(--color-text-muted); margin-top: 0.15rem; max-width: 34rem; }
	.mono { font-family: var(--font-mono); font-size: 0.85rem; }
	.muted { color: var(--color-text-muted); }
	.small { font-size: 0.8rem; }
	.scenario { color: var(--color-cyan); }

	/* iteration sub-table nested in a suite's detail row */
	.sub-cell { padding: 0 0 0.4rem 1.2rem; background: var(--color-bg-secondary, rgba(0,0,0,0.12)); }
	.itable th, .itable td { padding: 0.35rem 0.6rem; border-bottom: 1px solid var(--color-border); font-size: 0.82rem; text-align: left; vertical-align: top; }
	.itable th { color: var(--color-text-muted); font-weight: 600; font-size: 0.74rem; }
	.scenario-row { cursor: pointer; }
	.scenario-row:hover { background: var(--color-bg-tertiary); }
	.iter-row { cursor: pointer; }
	.iter-row:hover { background: var(--color-bg-tertiary); }

	/* conversation detail nested in an iteration's detail row */
	.conv-cell { padding: 0.5rem 0.6rem 0.8rem 1.6rem; background: var(--color-bg-tertiary); }
	.q { margin-bottom: 0.3rem; font-size: 0.88rem; }
	.meta { margin-bottom: 0.6rem; }
	.asserts { margin-bottom: 0.7rem; }
	.assert { display: flex; gap: 0.4rem; align-items: baseline; padding: 0.1rem 0; font-size: 0.78rem; }
	.assert .mark { width: 1rem; flex-shrink: 0; }
	.assert.ok .mark { color: var(--color-success); }
	.assert.bad .mark { color: var(--color-error); }
	.assert.mid .mark { color: var(--color-warning, #c80); }
	/* DEFER/REFLECTS: the mark IS the 0-100 measurement, not a checkmark */
	.assert.defer .mark.score { width: auto; min-width: 1.6rem; font-weight: 700; font-variant-numeric: tabular-nums; }
	.assert.defer .amsg { color: var(--color-text-muted, #888); }
	/* Judge rationale: one terse sentence under the DEFER row explaining the score */
	.judge-reason {
		margin: 0.1rem 0 0.3rem 1.6rem; padding-left: 0.5rem;
		border-left: 2px solid var(--color-border);
		color: var(--color-text-muted, #888); font-size: 0.82rem; line-height: 1.35;
		font-style: italic; max-width: 60rem;
	}
	.amsg { color: var(--color-error); }
	.convo { display: flex; flex-direction: column; gap: 0.25rem; max-width: 60rem; }
	.ev { padding: 0.25rem 0.45rem; border-left: 2px solid var(--color-border); font-size: 0.83rem; }
	.ev .evtag { font-family: var(--font-mono); font-size: 0.7rem; color: var(--color-text-muted); margin-right: 0.4rem; text-transform: uppercase; }
	.ev .md { margin-top: 0.25rem; }
	.ev .md :global(p) { margin: 0 0 0.5rem; }
	.ev .md :global(p:last-child) { margin-bottom: 0; }
	.ev .md :global(ul), .ev .md :global(ol) { margin: 0.25rem 0 0.5rem; padding-left: 1.2rem; }
	.ev .md :global(li) { margin: 0.1rem 0; }
	.ev .md :global(h1), .ev .md :global(h2), .ev .md :global(h3), .ev .md :global(h4) { font-size: 0.92rem; font-weight: 600; margin: 0.5rem 0 0.25rem; }
	.ev .md :global(code) { font-family: var(--font-mono); font-size: 0.82em; background: var(--color-bg-tertiary); padding: 0.05rem 0.25rem; border-radius: 3px; }
	.ev .md :global(pre) { background: var(--color-bg-tertiary); padding: 0.5rem; border-radius: 4px; overflow-x: auto; }
	.ev .md :global(strong) { font-weight: 600; }
	.ev .md-inline :global(code) { font-family: var(--font-mono); font-size: 0.82em; background: var(--color-bg-tertiary); padding: 0.05rem 0.25rem; border-radius: 3px; }
	.ev .md-inline :global(strong) { font-weight: 600; }
	.ev .md-inline :global(a) { color: var(--color-cyan); }
	.copy-tid { background: none; border: none; cursor: pointer; font-size: 0.75rem; padding: 0 0.25rem; opacity: 0.6; }
	.copy-tid:hover { opacity: 1; }
	/* Inline expand/collapse for a finding's full captured content */
	.finding-toggle {
		background: none; border: none; cursor: pointer; padding: 0 0.3rem;
		color: var(--color-cyan); font-size: 0.78rem; font-family: var(--font-sans);
	}
	.finding-toggle:hover { text-decoration: underline; }
	/* Collapsed synthesis: cap height and fade the cut edge so the truncation reads
	   as deliberate. Expanding (openSynthesis) drops .collapsed and shows the full text. */
	.synth-body.collapsed {
		max-height: 12em; overflow: hidden;
		-webkit-mask-image: linear-gradient(to bottom, black 70%, transparent 100%);
		mask-image: linear-gradient(to bottom, black 70%, transparent 100%);
	}
	/* Download action for a spilled artifact, sits next to the more/less toggle. */
	.artifact-dl {
		color: var(--color-cyan); font-size: 0.78rem; font-family: var(--font-sans);
		text-decoration: none; padding: 0 0.3rem; border-bottom: 1px dotted var(--color-cyan);
	}
	.artifact-dl:hover { border-bottom-style: solid; }
	a.agent-link { color: var(--color-cyan); text-decoration: none; border-bottom: 1px dotted var(--color-cyan); }
	a.agent-link:hover { text-decoration: none; border-bottom-style: solid; }
	.ev.synthesis { border-left-color: var(--color-cyan); background: var(--color-cyan-bg, rgba(0,180,220,0.08)); }
	.ev.err { border-left-color: var(--color-error); color: var(--color-error); }
	.sig { font-family: var(--font-mono); font-size: 0.7rem; margin-right: 0.3rem; }
	.sig-agree { color: var(--color-success); }
	.sig-concern, .sig-block { color: var(--color-error); }
	.sig-stand_aside { color: var(--color-text-muted); }

	.badge { display: inline-block; padding: 0.1rem 0.45rem; border-radius: 4px; font-size: 0.7rem; font-weight: 600; text-transform: uppercase; }
	.phase-pass { background: var(--color-success-bg); color: var(--color-success); }
	.phase-fail { background: var(--color-error-bg); color: var(--color-error); }
	.phase-neutral { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.phase-running { background: var(--color-cyan-bg); color: var(--color-cyan); }
	/* REFLECTS quality verdict pill on the scenario row: colour by band so a low
	   score reads as a failure, not a pass. No checkmark — the number is the verdict. */
	.score-pill {
		display: inline-block; min-width: 1.7rem; text-align: center;
		margin-left: 0.4rem; padding: 0.05rem 0.4rem; border-radius: 4px;
		font-weight: 700; font-size: 0.78rem; font-variant-numeric: tabular-nums;
	}
	.score-pill.ok { background: var(--color-success-bg); color: var(--color-success); }
	.score-pill.mid { background: var(--color-warning-bg, #3a2f0a); color: var(--color-warning, #c80); }
	.score-pill.bad { background: var(--color-error-bg); color: var(--color-error); }
	.running-badge { display: inline-flex; align-items: center; gap: 0.35rem; }
	.spin {
		display: inline-block; width: 0.7rem; height: 0.7rem; border-radius: 50%;
		border: 2px solid var(--color-cyan); border-top-color: transparent;
		animation: spin 0.7s linear infinite;
	}
	@keyframes spin { to { transform: rotate(360deg); } }
	.iter-row.running { cursor: default; }
	.iter-row.running:hover { background: transparent; }
	.ev.standaside-note { font-style: italic; }
	.phase-pending { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.passed { color: var(--color-success); font-weight: 600; }
	.failed { color: var(--color-error); font-weight: 600; }
	.errored { color: var(--color-warning, #c66); font-weight: 600; }
	.dl { color: var(--color-cyan); text-decoration: none; font-family: var(--font-mono); font-size: 0.78rem; padding: 0.08rem 0.35rem; border: 1px solid var(--color-cyan); border-radius: 4px; }
	.dl:hover { text-decoration: underline; }
	.del-btn { margin-left: 0.5rem; background: none; border: none; color: var(--color-text-muted, #888); cursor: pointer; font-size: 0.85rem; line-height: 1; padding: 0 0.25rem; }
	.del-btn:hover { color: #e5534b; }
	.copy-btn { margin-right: 0.5rem; background: none; border: none; cursor: pointer; font-size: 0.85rem; line-height: 1; padding: 0 0.25rem; }
	.copy-btn:hover { opacity: 0.7; }

	/* Copyable, scrollable, resizable error modal (replaces native alert()). */
	.err-overlay {
		position: fixed; inset: 0; z-index: 1000; display: flex;
		align-items: center; justify-content: center;
		background: rgba(0, 0, 0, 0.5); padding: 1.5rem;
	}
	.err-modal {
		display: flex; flex-direction: column;
		background: var(--color-bg-secondary, #1b1f27);
		border: 1px solid var(--color-error, #e5534b); border-radius: 8px;
		width: min(720px, 92vw); max-height: 80vh; box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
	}
	.err-head {
		display: flex; align-items: center; justify-content: space-between;
		padding: 0.6rem 0.9rem; border-bottom: 1px solid var(--color-border, #333);
	}
	.err-title { color: var(--color-error, #e5534b); font-weight: 600; }
	.err-actions { display: flex; gap: 0.5rem; }
	.err-btn {
		background: none; border: 1px solid var(--color-border, #444);
		color: var(--color-text, #ddd); cursor: pointer; border-radius: 4px;
		font-size: 0.8rem; padding: 0.2rem 0.55rem;
	}
	.err-btn:hover { border-color: var(--color-cyan, #4aa); color: var(--color-cyan, #4aa); }
	.err-body {
		margin: 0; padding: 0.9rem; overflow: auto; flex: 1;
		white-space: pre-wrap; word-break: break-word;
		font-family: var(--font-mono); font-size: 0.8rem; line-height: 1.45;
		color: var(--color-text, #ddd); resize: vertical;
	}
</style>
