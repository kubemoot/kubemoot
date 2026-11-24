package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import ai.kubemoot.agent.nats.BusReader.BusMessage;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import java.util.stream.IntStream;
import org.junit.jupiter.api.Test;

class ThreadStateCountersTest {

    @Test
    void theDiscussSequenceOnlyRisesEvenWhenUpdatedFromManyThreads() {
        var state = new ThreadState("t1");
        IntStream.rangeClosed(1, 2000).parallel().forEach(state::raiseDiscussSeq);
        state.raiseDiscussSeq(5);
        assertEquals(2000, state.discussSeq);
    }

    @Test
    void synthesisTokensAddUpWithoutLosingAnUpdate() {
        var state = new ThreadState("t1");
        IntStream.range(0, 2000).parallel().forEach(i -> state.addSynthesisTokens(3, 2));
        assertEquals(6000, state.synthesisInputTokens);
        assertEquals(4000, state.synthesisOutputTokens);
    }

    @Test
    void aBusMessageIsEqualByItsBytesNotTheirArrayIdentity() {
        var a = new BusMessage(7, "s", new byte[] {1, 2});
        var b = new BusMessage(7, "s", new byte[] {1, 2});
        assertEquals(a, b);
        assertEquals(a.hashCode(), b.hashCode());
        assertNotEquals(a, new BusMessage(7, "s", new byte[] {1, 3}));
        assertNotEquals(a, new BusMessage(8, "s", new byte[] {1, 2}));
        assertNotEquals(a, new BusMessage(7, "x", new byte[] {1, 2}));
        assertTrue(a.toString().contains("[1, 2]"));
    }
}
