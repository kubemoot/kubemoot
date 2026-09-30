package ai.kubemoot.agent.config;

/**
 * The default phase budgets, in seconds, declared once. The config mapping uses
 * them as its defaults, and the discussion orchestrator falls back to the same
 * values when a configured budget is zero or negative, so the two cannot drift.
 */
public final class PhaseBudgetDefaults {

    public static final String ADVISORY_SECONDS = "10";
    public static final String EVALUATION_SECONDS = "300";
    public static final String REVIEW_SECONDS = "15";
    public static final String SYNTHESIS_SECONDS = "90";

    private PhaseBudgetDefaults() {}

    /** {@code configured} when positive, otherwise the declared default. */
    public static int orDefault(int configured, String declaredDefault) {
        return configured > 0 ? configured : Integer.parseInt(declaredDefault);
    }
}
