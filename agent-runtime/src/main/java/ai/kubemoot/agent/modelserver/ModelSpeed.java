package ai.kubemoot.agent.modelserver;

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
 */
public record ModelSpeed(long loadMs, long promptTokensPerSecond, long generateTokensPerSecond,
                         long observedGenerateTokensPerSecond, int observedSamples) {

    /** The generation speed to plan with: observed once there is enough of it, else calibrated. */
    public long generateSpeed() {
        return observedGenerateTokensPerSecond > 0 ? observedGenerateTokensPerSecond : generateTokensPerSecond;
    }
}
