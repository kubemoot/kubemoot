package ai.kubemoot.agent.config;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class PhaseBudgetDefaultsTest {

    @Test
    void positiveBudget_isKept() {
        assertEquals(42, PhaseBudgetDefaults.orDefault(42, PhaseBudgetDefaults.EVALUATION_SECONDS));
    }

    @Test
    void zeroOrNegativeBudget_fallsBackToTheDeclaredDefault() {
        assertEquals(300, PhaseBudgetDefaults.orDefault(0, PhaseBudgetDefaults.EVALUATION_SECONDS),
                "the fallback is the same value the config mapping declares");
        assertEquals(10, PhaseBudgetDefaults.orDefault(-1, PhaseBudgetDefaults.ADVISORY_SECONDS));
        assertEquals(15, PhaseBudgetDefaults.orDefault(0, PhaseBudgetDefaults.REVIEW_SECONDS));
        assertEquals(90, PhaseBudgetDefaults.orDefault(0, PhaseBudgetDefaults.SYNTHESIS_SECONDS));
    }
}
