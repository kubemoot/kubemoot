package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Trivial shape test for {@link NoFitException} — verifies it carries
 * the predictor reasoning forward so DiscussionSubscriber can include
 * it in the stand_aside signal's metadata.
 *
 * The integration behavior (ChatService throwing NoFitException when
 * the predictor refuses all candidates, DiscussionSubscriber catching
 * and publishing stand_aside with reason="no-fit") is exercised in the
 * live cluster — no clean unit-test path without a full Quarkus + NATS
 * harness, and the contract is small enough that grep-by-name plus a
 * smoke test post-deploy is the right cost/benefit.
 */
class NoFitExceptionTest {

    @Test
    void preservesPredictorReason() {
        var nfe = new NoFitException("warm-saturated everywhere; loaded 23+KV 3 > 24");
        assertEquals("warm-saturated everywhere; loaded 23+KV 3 > 24", nfe.predictorReason());
        assertTrue(nfe.getMessage().contains("warm-saturated everywhere"));
    }

    @Test
    void isRuntimeException_caughtByCatch_Exception() {
        // Sanity: must extend RuntimeException so a `catch (Exception)`
        // can route it (and our earlier catch clause for NoFitException
        // can intercept it before the generic catch fires).
        assertTrue(RuntimeException.class.isAssignableFrom(NoFitException.class));
    }
}
