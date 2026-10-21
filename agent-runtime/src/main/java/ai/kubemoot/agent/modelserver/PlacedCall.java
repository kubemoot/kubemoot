package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

/**
 * One placed call from placement to completion: the {@link PlacementDecision} that put
 * it on a server, and what its requests measured there. The first request is the one
 * the prediction was made for, so it is scored against the prediction; later requests
 * of a tool loop only count.
 *
 * <p>{@link #snapshot} is the call as the bus contract describes it
 * ({@code placement-call.json}): the discussion receipt and the agent's signal carry
 * it, and the performance history keeps the compact form.</p>
 *
 * <p>The tool loop sends from one thread and the signal is built on another, so every
 * method is synchronized.</p>
 */
public final class PlacedCall {

    /** Longest reason kept on a snapshot. */
    static final int REASON_MAX_CHARS = 600;
    private static final String PHASE_TRIAGE = "triage";
    private static final String PHASE_EVALUATION = "evaluation";

    /**
     * How far the prediction was from the first request, in milliseconds. A first-token
     * figure is -1 when the request was not streamed. Relative errors are the absolute
     * error over the measured time.
     */
    public record Score(String policy, String server, String model, String kind, long predictedTotalMs,
                        long actualTotalMs, long totalAbsMs, long totalSignedMs, double totalRelative,
                        long firstTokenAbsMs, double firstTokenRelative) {
        /** True when the request was streamed, so its time to the first token was measured. */
        public boolean hasFirstToken() {
            return firstTokenAbsMs >= 0;
        }
    }

    private final String server;
    private final String model;
    private final PlacementDecision decision;
    private long waitedMs;
    private CallObservation first;
    private int requests;
    private Score score;

    public PlacedCall(String server, String model, PlacementDecision decision) {
        this.server = server;
        this.model = model;
        this.decision = decision;
    }

    /** The call for a placement; null when the placement has no decision to follow. */
    public static PlacedCall of(CallPlanner.Placement placement) {
        if (placement == null || placement.decision() == null) {
            return null;
        }
        return new PlacedCall(placement.pick().server().name(), placement.model(), placement.decision());
    }

    /** This call as planned at selection: see {@link PlacementDecision#plannedAtSelection}. */
    public PlacedCall plannedAtSelection() {
        return new PlacedCall(server, model, decision.plannedAtSelection());
    }

    /** The call waited {@code ms} for capacity before it was placed. */
    public synchronized void waited(long ms) {
        waitedMs = Math.max(ms, 0L);
    }

    /** The policy that placed the call. */
    public String policy() {
        return decision.policy();
    }

    /**
     * Takes in one request the placement sent. The first one is scored when it
     * completed and the placement predicted a time; the score is returned once.
     */
    public synchronized Optional<Score> observe(CallObservation o) {
        requests++;
        if (first != null) {
            return Optional.empty();
        }
        first = o;
        score = scoreOf(o);
        return Optional.ofNullable(score);
    }

    private Score scoreOf(CallObservation o) {
        Prediction p = decision.predicted();
        if (p == null || !o.ok() || o.durationMs() < 0) {
            return null;
        }
        long predicted = totalMs(p);
        long signed = o.durationMs() - predicted;
        long firstAbs = -1;
        double firstRel = 0.0;
        if (o.firstTokenMs() >= 0) {
            firstAbs = Math.abs(o.firstTokenMs() - firstTokenMs(p));
            firstRel = relative(firstAbs, o.firstTokenMs());
        }
        return new Score(decision.policy(), server, model, o.kind(), predicted, o.durationMs(), Math.abs(signed),
                signed, relative(Math.abs(signed), o.durationMs()), firstAbs, firstRel);
    }

    /** {@code error} over {@code actual}, with an actual under a millisecond counted as one. */
    private static double relative(long error, long actual) {
        return error / (double) Math.max(actual, 1L);
    }

    // ---- the call as the bus contract describes it ----

    /**
     * The call as of now. {@code detailed} adds the reason and the alternatives, which
     * the performance history leaves out to stay small.
     */
    public synchronized Map<String, Object> snapshot(boolean detailed) {
        Map<String, Object> m = new LinkedHashMap<>();
        String kind = first == null ? null : first.kind();
        m.put("phase", CallObservation.KIND_TRIAGE.equals(kind) ? PHASE_TRIAGE : PHASE_EVALUATION);
        putIfSet(m, "kind", kind);
        if (first != null) {
            m.put("at", first.atMs());
            m.put("coldStart", first.coldStart());
        }
        m.put("model", model);
        m.put("server", server);
        m.put("policy", decision.policy());
        if (detailed) {
            putDetail(m);
        }
        if (decision.predicted() != null) {
            m.put("predicted", predictedMap(decision.predicted()));
        }
        if (first != null) {
            m.put("actual", actualMap(first));
        }
        if (score != null) {
            m.put("error", errorMap(score));
        }
        return m;
    }

    private void putDetail(Map<String, Object> m) {
        String reason = decision.reason();
        if (!reason.isEmpty()) {
            m.put("reason", reason.length() <= REASON_MAX_CHARS ? reason : reason.substring(0, REASON_MAX_CHARS));
        }
        if (!decision.alternatives().isEmpty()) {
            List<Map<String, Object>> alternatives = new ArrayList<>();
            for (PlacementDecision.Alternative a : decision.alternatives()) {
                alternatives.add(alternativeMap(a));
            }
            m.put("alternatives", alternatives);
        }
    }

    private Map<String, Object> alternativeMap(PlacementDecision.Alternative a) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("server", a.server());
        if (a.model() != null && !a.model().equals(model)) {
            m.put("model", a.model());
        }
        m.put("warm", a.warm());
        m.put("startsNow", a.startsNow());
        if (a.predicted() != null) {
            m.put("predicted", predictedMap(a.predicted()));
        }
        return m;
    }

    private static Map<String, Object> predictedMap(Prediction p) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("waitMs", millis(p.queue()));
        m.put("loadMs", millis(p.load()));
        m.put("promptMs", millis(p.prompt()));
        m.put("generateMs", millis(p.generate()));
        m.put("firstTokenMs", firstTokenMs(p));
        m.put("totalMs", totalMs(p));
        return m;
    }

    private Map<String, Object> actualMap(CallObservation o) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("ok", o.ok());
        m.put("waitMs", waitedMs);
        if (o.firstTokenMs() >= 0) {
            m.put("firstTokenMs", o.firstTokenMs());
        }
        m.put("totalMs", Math.max(o.durationMs(), 0L));
        m.put("inputTokens", o.promptTokens());
        m.put("outputTokens", o.outputTokens());
        m.put("requests", requests);
        return m;
    }

    private static Map<String, Object> errorMap(Score s) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("totalAbsMs", s.totalAbsMs());
        m.put("totalRelative", round(s.totalRelative()));
        m.put("totalSignedMs", s.totalSignedMs());
        if (s.hasFirstToken()) {
            m.put("firstTokenAbsMs", s.firstTokenAbsMs());
            m.put("firstTokenRelative", round(s.firstTokenRelative()));
        }
        return m;
    }

    private static void putIfSet(Map<String, Object> m, String key, String value) {
        if (value != null && !value.isEmpty()) {
            m.put(key, value);
        }
    }

    private static long millis(double seconds) {
        return Math.round(seconds * 1000.0);
    }

    /** The predicted total: the sum of the rounded parts, so the parts add up to it exactly. */
    private static long totalMs(Prediction p) {
        return millis(p.queue()) + millis(p.load()) + millis(p.prompt()) + millis(p.generate());
    }

    private static long firstTokenMs(Prediction p) {
        return millis(p.queue()) + millis(p.load()) + millis(p.prompt());
    }

    private static double round(double v) {
        return Math.round(v * 1000.0) / 1000.0;
    }
}
