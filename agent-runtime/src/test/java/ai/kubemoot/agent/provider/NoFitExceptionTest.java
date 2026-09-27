package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Trivial shape test for {@link NoFitException} — verifies it carries
 * the predictor reasoning forward so DiscussionSubscriber can include
 * it in the stand_aside signal's metadata.
 *
 * The integration behavior (ChatService throwing NoFitException with a
 * gpu-busy or model-too-large reason, DiscussionSubscriber publishing the
 * stand_aside) is covered by ChatServiceCapacityTest and
 * DiscussionSubscriberHelpersTest.
 */
class NoFitExceptionTest {

    @Test
    void preservesPredictorReason() {
        var nfe = new NoFitException("warm-saturated everywhere; loaded 23+KV 3 > 24");
        assertEquals("warm-saturated everywhere; loaded 23+KV 3 > 24", nfe.predictorReason());
        assertTrue(nfe.getMessage().contains("warm-saturated everywhere"));
    }

    @Test
    void singleArgConstructor_defaultsToGpuBusy() {
        var nfe = new NoFitException("busy");
        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertEquals("", nfe.model());
    }

    @Test
    void factories_carryReasonAndModel() {
        var busy = NoFitException.gpuBusy("qwen3:14b", "all busy");
        assertEquals("gpu-busy", busy.reason());
        assertEquals("qwen3:14b", busy.model());
        var large = NoFitException.modelTooLarge("qwen3:235b", "too big");
        assertEquals("model-too-large", large.reason());
        assertEquals("qwen3:235b", large.model());
        assertTrue(large.getMessage().contains("model-too-large"));
    }

    @Test
    void isRuntimeException_caughtByCatch_Exception() {
        // Sanity: must extend RuntimeException so a `catch (Exception)`
        // can route it (and our earlier catch clause for NoFitException
        // can intercept it before the generic catch fires).
        assertTrue(RuntimeException.class.isAssignableFrom(NoFitException.class));
    }
}
