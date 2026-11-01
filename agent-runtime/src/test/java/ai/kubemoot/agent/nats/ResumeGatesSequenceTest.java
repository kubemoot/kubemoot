package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** A message held behind a gate keeps the stream sequence it arrived with. */
class ResumeGatesSequenceTest {

    @Test
    void heldMessagesKeepTheirSequencesAndOrder() {
        var gates = new ResumeGates();
        gates.shut("t1");

        assertTrue(gates.hold("t1", "a", 11));
        assertTrue(gates.hold("t1", "b", 12));
        var applied = new ArrayList<ResumeGates.Held>();
        gates.openHeld("t1", applied::add);

        assertEquals(List.of(new ResumeGates.Held("a", 11), new ResumeGates.Held("b", 12)), applied);
        assertFalse(gates.hold("t1", "c", 13), "after the gate opened, messages pass straight through");
    }

    @Test
    void aMessageHeldWithoutASequenceHasSequenceZero() {
        var gates = new ResumeGates();
        gates.shut("t1");
        gates.hold("t1", "a");
        var applied = new ArrayList<ResumeGates.Held>();

        gates.openHeld("t1", applied::add);

        assertEquals(List.of(new ResumeGates.Held("a", 0)), applied);
    }
}
