// Fills a NATS JetStream KUBEMOOT_DISCUSS stream with a day of synthetic discussion
// traffic, and times the dashboard's history endpoint against it. Content-free: every
// message is generated text, nothing is read from a cluster.
//
//   nats-server -js -p 4333 -sd /tmp/fixture-store &
//   node scripts/history-fixture.mjs load nats://127.0.0.1:4333 5000
//   NATS_URL=nats://127.0.0.1:4333 PORT=3999 node build &       # the built dashboard
//   node scripts/history-fixture.mjs measure http://127.0.0.1:3999/dashboard
//
// `load` creates the stream with the chart's default limits (24h, the message count
// given as the cap) and publishes the messages in order, about twelve per thread across
// three crews. `measure` asks for the page's reads 15 times each and prints the median
// and worst time, the response size and the message count.
import { connect, JSONCodec } from 'nats';

const THREAD_SIZE = 12;
const CREWS = [
	['team-a', 'pilot'],
	['team-a', 'ops'],
	['team-b', 'pilot']
];
const TYPES = ['thread_start', 'advisory', 'agree', 'agree', 'concern', 'agree', 'stand_aside', 'agree', 'proposal', 'agree', 'synthesis', 'thread_close'];

/** A small deterministic generator, so every run loads the same traffic. */
function prng(seed) {
	let s = seed;
	return () => {
		s = (s * 1664525 + 1013904223) % 4294967296;
		return s / 4294967296;
	};
}

const WORDS = 'cluster node pod volume network latency model accelerator memory queue stream bucket crew agent signal thread answer reason'.split(' ');

function text(rand, chars) {
	let out = '';
	while (out.length < chars) out += WORDS[Math.floor(rand() * WORDS.length)] + ' ';
	return out.trim();
}

function body(type, rand) {
	if (type === 'thread_start') return text(rand, 160);
	if (type === 'synthesis') return text(rand, 3000);
	if (type === 'thread_close') return text(rand, 400);
	return text(rand, 900 + Math.floor(rand() * 600));
}

export function* messages(count) {
	const rand = prng(42);
	const start = Date.now() - 23 * 3600 * 1000;
	const span = (23 * 3600 * 1000) / count;
	for (let i = 0; i < count; i++) {
		const thread = Math.floor(i / THREAD_SIZE);
		const [ns, crew] = CREWS[thread % CREWS.length];
		const type = TYPES[i % THREAD_SIZE];
		const threadId = `thread-${thread}`;
		yield {
			subject: `kubemoot.discuss.${ns}.${crew}.general.${threadId}`,
			payload: {
				messageId: `m-${i}`,
				threadId,
				agentName: type === 'thread_start' || type === 'synthesis' ? 'coordinator' : `agent-${i % 5}`,
				messageType: type,
				content: body(type, rand),
				channel: 'general',
				timestamp: new Date(start + i * span).toISOString(),
				metadata: { crew, userQuery: type === 'thread_start' ? text(rand, 120) : undefined, inputTokens: 1200, outputTokens: 340, model: 'qwen3-32b', server: 'vllm-rig1' }
			}
		};
	}
}

async function load(url, count) {
	const nc = await connect({ servers: url });
	const jsm = await nc.jetstreamManager();
	try {
		await jsm.streams.delete('KUBEMOOT_DISCUSS');
	} catch {
		// first load
	}
	await jsm.streams.add({ name: 'KUBEMOOT_DISCUSS', subjects: ['kubemoot.discuss.>'], max_age: 24 * 3600 * 1e9, max_msgs: count, max_bytes: 1073741824 });
	const js = nc.jetstream();
	const jc = JSONCodec();
	let pending = [];
	for (const m of messages(count)) {
		pending.push(js.publish(m.subject, jc.encode(m.payload)));
		if (pending.length === 200) {
			await Promise.all(pending);
			pending = [];
		}
	}
	await Promise.all(pending);
	const info = await jsm.streams.info('KUBEMOOT_DISCUSS');
	console.log(`loaded ${info.state.messages} messages, ${info.state.bytes} bytes, ${Math.ceil(count / THREAD_SIZE)} threads`);
	await nc.close();
}

async function timed(url) {
	const t0 = performance.now();
	const res = await fetch(url);
	const text = await res.text();
	const ms = performance.now() - t0;
	const body = JSON.parse(text);
	return { ms, bytes: Buffer.byteLength(text), messages: body.messages.length, server: res.headers.get('server-timing') };
}

const median = (xs) => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)];

async function measure(base, label, path) {
	const runs = [];
	await timed(base + path); // warm the connection and the server's code
	for (let i = 0; i < 15; i++) runs.push(await timed(base + path));
	const ms = runs.map((r) => r.ms);
	const last = runs.at(-1);
	console.log(
		`${label.padEnd(34)} median ${median(ms).toFixed(0).padStart(5)} ms  worst ${Math.max(...ms).toFixed(0).padStart(5)} ms  ${String(last.messages).padStart(5)} messages  ${(last.bytes / 1024).toFixed(0).padStart(6)} KiB  ${last.server ?? ''}`
	);
}

const [, , mode, a, b, c] = process.argv;
if (mode === 'load') await load(a, Number(b));
else if (mode === 'measure') {
	const api = '/api/nats/history?stream=KUBEMOOT_DISCUSS';
	const all = encodeURIComponent('kubemoot.discuss.>');
	const thread = encodeURIComponent(c ?? 'kubemoot.discuss.team-a.pilot.general.thread-3');
	for (const spec of (b ?? 'old,new').split(',')) {
		if (spec === 'old') await measure(a, 'before: newest 1000, whole stream', `${api}&subject=${all}&limit=1000`);
		if (spec === 'new') {
			await measure(a, 'after: list window (200)', `${api}&subject=${all}&limit=200`);
			await measure(a, 'after: one thread by subject', `${api}&subject=${thread}&limit=500`);
		}
	}
}
