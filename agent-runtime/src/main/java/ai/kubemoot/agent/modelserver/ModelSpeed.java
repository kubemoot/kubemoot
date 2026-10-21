package ai.kubemoot.agent.modelserver;

import java.util.ArrayList;
import java.util.List;

/**
 * What a model costs on a server, as the operator publishes it in
 * {@link ServerState#modelSpeeds()}: what calibration measured, and how fast crew
 * calls that ran alone on the server have generated since. Speeds are tokens per
 * second; zero means not measured.
 *
 * @param loadMs                          calibrated load time, milliseconds
 * @param promptTokensPerSecond           calibrated prompt processing speed
 * @param generateTokensPerSecond         calibrated generation speed of one request
 * @param observedGenerateTokensPerSecond median generation speed of solo crew calls since
 *                                        the calibration; zero with too few to trust
 * @param observedSamples                 how many calls that median is over
 * @param loadFactors                     how much slower calls ran as more shared the server, from
 *                                        the crew calls since the calibration; empty with too few
 */
public record ModelSpeed(long loadMs, long promptTokensPerSecond, long generateTokensPerSecond,
                         long observedGenerateTokensPerSecond, int observedSamples, List<LoadFactor> loadFactors) {

    /**
     * Calls ran {@code factor} times as long, at the median, once at least
     * {@code activeCalls} calls held the server as they started (themselves included).
     */
    public record LoadFactor(int activeCalls, double factor) {}

    public ModelSpeed {
        loadFactors = loadFactors == null ? List.of() : List.copyOf(loadFactors);
    }

    /** A speed with no load factors. */
    public ModelSpeed(long loadMs, long promptTokensPerSecond, long generateTokensPerSecond,
                      long observedGenerateTokensPerSecond, int observedSamples) {
        this(loadMs, promptTokensPerSecond, generateTokensPerSecond, observedGenerateTokensPerSecond,
                observedSamples, List.of());
    }

    /** The load factors in a {@code loadFactors} JSON array; empty when absent or malformed. */
    static List<LoadFactor> loadFactors(com.fasterxml.jackson.databind.JsonNode node) {
        List<LoadFactor> out = new ArrayList<>();
        if (node.isArray()) {
            node.forEach(f -> {
                if (f.path("activeCalls").asInt(0) > 1 && f.path("factor").asDouble(0.0) > 0.0) {
                    out.add(new LoadFactor(f.path("activeCalls").asInt(), f.path("factor").asDouble()));
                }
            });
        }
        return out;
    }

    /**
     * How many times as long a call takes when {@code calls} calls hold the server as it
     * starts, itself included: the factor of the highest level reached, 1 when none is.
     */
    public double slowdown(long calls) {
        double factor = 1.0;
        int level = 0;
        for (LoadFactor f : loadFactors) {
            if (f.activeCalls() <= calls && f.activeCalls() > level) {
                level = f.activeCalls();
                factor = f.factor();
            }
        }
        return Math.max(factor, 1.0);
    }

    /** The generation speed to plan with: observed once there is enough of it, else calibrated. */
    public long generateSpeed() {
        return observedGenerateTokensPerSecond > 0 ? observedGenerateTokensPerSecond : generateTokensPerSecond;
    }
}
