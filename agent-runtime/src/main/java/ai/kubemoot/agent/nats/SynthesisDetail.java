package ai.kubemoot.agent.nats;

import java.util.List;
import java.util.Map;

/**
 * What one synthesis did with a thread's per-call evidence, kept for the synthesis event: the keys the
 * analysts covered, the keys they did not, the analysts convened to read more, and the milliseconds
 * each step took (catalog, read, convene, each model round).
 */
record SynthesisDetail(List<String> covered, List<String> uncovered, List<String> convened,
                       Map<String, Long> stepMillis) {}
