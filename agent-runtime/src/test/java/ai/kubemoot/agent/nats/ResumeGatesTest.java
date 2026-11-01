package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The gate that holds a resuming thread's live messages until the ones it missed are applied. */
class ResumeGatesTest {

    @Test
    void aThreadWithNoGateIsNotHeld() {
        var gates = new ResumeGates();

        assertFalse(gates.hold("t1", "m"), "no gate: the message goes straight through");
    }

    @Test
    void aShutGateQueuesMessagesAndOpeningItHandsThemOverInOrder() {
        var gates = new ResumeGates();
        gates.shut("t1");

        assertTrue(gates.hold("t1", "a"));
        assertTrue(gates.hold("t1", "b"));
        assertTrue(gates.isShut("t1"));
        var applied = new ArrayList<String>();
        gates.open("t1", applied::add);

        assertEquals(List.of("a", "b"), applied);
        assertFalse(gates.isShut("t1"));
        assertFalse(gates.hold("t1", "c"), "after the gate opened, messages pass straight through");
    }

    @Test
    void gatesAreOnePerThread() {
        var gates = new ResumeGates();
        gates.shut("t1");

        assertFalse(gates.hold("t2", "m"), "another thread is not held");
    }

    @Test
    void aMessageThatArrivesWhileTheQueueIsBeingHandedOverIsAppliedAfterTheOnesAlreadyQueued() {
        var gates = new ResumeGates();
        gates.shut("t1");
        gates.hold("t1", "first");
        var applied = new ArrayList<String>();

        gates.open("t1", message -> {
            applied.add(message);
            if ("first".equals(message)) {
                assertTrue(gates.hold("t1", "arrived-during"), "still queued, so the order of arrival is kept");
            }
        });

        assertEquals(List.of("first", "arrived-during"), applied);
    }

    @Test
    void openingAGateThatWasNeverShutDoesNothing() {
        var gates = new ResumeGates();
        var applied = new ArrayList<String>();

        gates.open("t1", applied::add);

        assertTrue(applied.isEmpty());
    }
}
