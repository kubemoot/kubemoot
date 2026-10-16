package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/**
 * What one round of a discussion thread cost and how it went, collected by the
 * coordinator as the thread's messages arrive and published on {@code thread_close}
 * as {@code metadata.receipt}: a snapshot at the close, so a message that arrives
 * after it is not counted. A reply that reopens the thread starts a new round and a
 * new receipt.
 *
 * <p>Times come from when the coordinator received each message. The phases end at
 * the advisory, the triage result, the last agent signal, and the close; agents may
 * signal before the triage result reaches the coordinator, so evaluation starts at
 * whichever of the selection's end and the first signal comes first. An agent's line
 * adds up every terminal signal it sent in the round and keeps the last one as its
 * signal; its wait is the time from its first {@code waiting} message to its next
 * {@code evaluating} or terminal signal. Messages may arrive on several NATS
 * dispatcher threads, so every method is synchronized.</p>
 */
final class DiscussionReceipt {

    static final String SIGNAL_AGREE = "agree";
    static final String SIGNAL_STAND_ASIDE = "stand_aside";
    static final String SIGNAL_FAILURE = "failure";
    /** Longest reason kept on an agent line. */
    static final int REASON_MAX_CHARS = 300;

    private static final Map<String, String> TERMINAL = Map.of(
            SIGNAL_AGREE, SIGNAL_AGREE, "contribution", SIGNAL_AGREE,
            "concern", "concern", "block", "block",
            SIGNAL_STAND_ASIDE, SIGNAL_STAND_ASIDE, "decline", SIGNAL_STAND_ASIDE,
            SIGNAL_FAILURE, SIGNAL_FAILURE);

    private Instant started;
    private Instant advisoryAt;
    private Instant triageAt;
    private Instant firstSignalAt;
    private Instant lastSignalAt;
    private final Map<String, AgentLine> agents = new HashMap<>();
    private final Map<String, Instant> waitingSince = new HashMap<>();
    private final Map<String, Long> waitMs = new HashMap<>();

    /** One agent's part in the round, added up over its terminal signals. */
    private static final class AgentLine {
        String signal;
        Instant at;
        String reason;
        long inputTokens;
        long outputTokens;
        long inferenceMs;
        final Set<String> models = new LinkedHashSet<>();
    }

    /** The token sums over the agent lines. */
    private record AgentTokens(long in, long out) {}

    DiscussionReceipt(Instant started) {
        this.started = started;
    }

    /** True when {@code type} is a terminal signal the receipt counts. */
    static boolean isTerminal(String type) {
        return type != null && TERMINAL.containsKey(type);
    }

    /** Starts a new round at {@code at}: the thread was reopened by a reply. */
    synchronized void startRound(Instant at) {
        started = at;
        advisoryAt = null;
        triageAt = null;
        firstSignalAt = null;
        lastSignalAt = null;
        agents.clear();
        waitingSince.clear();
        waitMs.clear();
    }

    /** Takes in one message of the thread, received at {@code at}. */
    synchronized void observe(String agent, String type, JsonNode msg, Instant at) {
        if (agent == null || type == null || at == null) {
            return;
        }
        switch (type) {
            case "advisory" -> advisoryAt = advisoryAt == null ? at : advisoryAt;
            case "triage_result" -> triageAt = triageAt == null ? at : triageAt;
            case "waiting" -> waitingSince.putIfAbsent(agent, at);
            case "evaluating" -> endWait(agent, at);
            default -> observeSignal(agent, type, msg, at);
        }
    }

    private void endWait(String agent, Instant at) {
        Instant since = waitingSince.remove(agent);
        if (since != null) {
            waitMs.merge(agent, millis(since, at), Long::sum);
        }
    }

    private void observeSignal(String agent, String type, JsonNode msg, Instant at) {
        String signal = TERMINAL.get(type);
        if (signal == null) {
            return;
        }
        endWait(agent, at);
        JsonNode meta = msg == null ? null : msg.get("metadata");
        AgentLine line = agents.computeIfAbsent(agent, k -> new AgentLine());
        line.signal = signal;
        line.at = at;
        line.reason = reasonOf(signal, meta);
        if (meta != null) {
            line.inputTokens += meta.path("inputTokens").asLong(0);
            line.outputTokens += meta.path("outputTokens").asLong(0);
            line.inferenceMs += meta.path("inferenceMs").asLong(0);
            meta.path("modelsUsed").forEach(m -> line.models.add(m.asText()));
        }
        if (firstSignalAt == null) {
            firstSignalAt = at;
        }
        if (lastSignalAt == null || at.isAfter(lastSignalAt)) {
            lastSignalAt = at;
        }
    }

    /** The reason a signal gave: its reason, or for a failure its last error; capped. */
    private static String reasonOf(String signal, JsonNode meta) {
        if (meta == null) {
            return null;
        }
        String reason = meta.hasNonNull("reason") ? meta.get("reason").asText() : null;
        if (reason == null && SIGNAL_FAILURE.equals(signal) && meta.hasNonNull("lastError")) {
            reason = meta.get("lastError").asText();
        }
        return reason == null || reason.length() <= REASON_MAX_CHARS ? reason
                : reason.substring(0, REASON_MAX_CHARS);
    }

    /**
     * The receipt as of {@code settled}. {@code selected} is how many agents the
     * coordinator selected (0 when it selected none); the convened count is the larger
     * of that and the agents that signalled. The coordinator's own tokens are added to
     * the totals.
     */
    synchronized ObjectNode build(ObjectMapper mapper, Instant settled, int selected,
                                  long coordinatorIn, long coordinatorOut) {
        ObjectNode r = mapper.createObjectNode();
        r.put("startedAt", started.toEpochMilli());
        r.put("settledAt", settled.toEpochMilli());
        r.put("totalMs", millis(started, settled));
        if (firstSignalAt != null) {
            r.put("firstSignalMs", millis(started, firstSignalAt));
        }
        r.set("phasesMs", phases(mapper, settled));
        r.set("consensus", consensus(mapper, selected));
        ArrayNode lines = r.putArray("agents");
        AgentTokens agentTokens = agentLines(lines, mapper);
        ObjectNode tokens = r.putObject("tokens");
        tokens.put("agentsIn", agentTokens.in());
        tokens.put("agentsOut", agentTokens.out());
        tokens.put("coordinatorIn", coordinatorIn);
        tokens.put("coordinatorOut", coordinatorOut);
        tokens.put("in", agentTokens.in() + coordinatorIn);
        tokens.put("out", agentTokens.out() + coordinatorOut);
        r.set("placements", placements(mapper));
        return r;
    }

    private ObjectNode phases(ObjectMapper mapper, Instant settled) {
        ObjectNode p = mapper.createObjectNode();
        Instant cursor = started;
        if (advisoryAt != null) {
            p.put("advisory", millis(cursor, advisoryAt));
            cursor = advisoryAt;
        }
        if (triageAt != null) {
            p.put("selection", millis(cursor, triageAt));
            cursor = triageAt;
        }
        if (lastSignalAt != null) {
            Instant evaluationStart = firstSignalAt.isBefore(cursor) ? firstSignalAt : cursor;
            p.put("evaluation", millis(evaluationStart, lastSignalAt));
            cursor = lastSignalAt.isAfter(cursor) ? lastSignalAt : cursor;
        }
        p.put("synthesis", millis(cursor, settled));
        return p;
    }

    private ObjectNode consensus(ObjectMapper mapper, int selected) {
        ObjectNode c = mapper.createObjectNode();
        c.put("convened", Math.max(selected, agents.size()));
        Map<String, Integer> counts = new TreeMap<>();
        agents.values().forEach(a -> counts.merge(a.signal, 1, Integer::sum));
        c.put(SIGNAL_AGREE, counts.getOrDefault(SIGNAL_AGREE, 0));
        c.put("concern", counts.getOrDefault("concern", 0));
        c.put("block", counts.getOrDefault("block", 0));
        c.put("standAside", counts.getOrDefault(SIGNAL_STAND_ASIDE, 0));
        c.put(SIGNAL_FAILURE, counts.getOrDefault(SIGNAL_FAILURE, 0));
        return c;
    }

    /** Writes one line per agent, ordered by when it last signalled; returns their token sums. */
    private AgentTokens agentLines(ArrayNode lines, ObjectMapper mapper) {
        long in = 0;
        long out = 0;
        List<Map.Entry<String, AgentLine>> ordered = new ArrayList<>(agents.entrySet());
        ordered.sort((a, b) -> a.getValue().at.compareTo(b.getValue().at));
        for (var e : ordered) {
            AgentLine a = e.getValue();
            lines.add(agentLine(mapper, e.getKey(), a));
            in += a.inputTokens;
            out += a.outputTokens;
        }
        return new AgentTokens(in, out);
    }

    private ObjectNode agentLine(ObjectMapper mapper, String agent, AgentLine a) {
        ObjectNode line = mapper.createObjectNode();
        line.put("agent", agent);
        line.put("signal", a.signal);
        line.put("atMs", millis(started, a.at));
        ArrayNode models = line.putArray("models");
        a.models.forEach(models::add);
        line.put("inputTokens", a.inputTokens);
        line.put("outputTokens", a.outputTokens);
        line.put("inferenceMs", a.inferenceMs);
        line.put("waitMs", waitMs.getOrDefault(agent, 0L));
        if (a.reason != null) {
            line.put("reason", a.reason);
        }
        return line;
    }

    /** How many agents' calls ran at each model@provider. */
    private ArrayNode placements(ObjectMapper mapper) {
        Map<String, Integer> counts = new TreeMap<>();
        agents.values().forEach(a -> a.models.forEach(m -> counts.merge(m, 1, Integer::sum)));
        ArrayNode out = mapper.createArrayNode();
        counts.forEach((place, n) -> {
            ObjectNode p = out.addObject();
            p.put("place", place);
            p.put("agents", n);
        });
        return out;
    }

    private static long millis(Instant from, Instant to) {
        return Math.max(0L, to.toEpochMilli() - from.toEpochMilli());
    }
}
