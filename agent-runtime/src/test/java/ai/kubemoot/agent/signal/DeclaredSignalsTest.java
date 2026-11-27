package ai.kubemoot.agent.signal;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

class DeclaredSignalsTest {

    @Test
    void theOperatorsListBecomesTheKindsInDeclarationOrder() {
        var signals = SignalTestKinds.consent3();
        assertEquals(List.of("agree", "concern", "question", "stand_aside", "tool_gap", "no_data", "block"),
                signals.all().stream().map(DeclaredSignal::name).toList());
        var question = signals.find("question");
        assertFalse(question.terminal(), "a question leaves the asker's turn open");
        assertTrue(question.addressed(), "a question is said to one agent");
        assertEquals(SignalEffect.QUESTION, question.effect());
        assertTrue(signals.isTerminal("agree"));
        assertFalse(signals.isTerminal("question"));
        assertFalse(signals.isTerminal("vote"), "an undeclared kind is not terminal");
    }

    @Test
    void absentBlankOrMalformedMeansNoSignalTool() {
        assertSame(DeclaredSignals.NONE, DeclaredSignals.parse(Optional.empty()));
        assertSame(DeclaredSignals.NONE, DeclaredSignals.parse("  "));
        assertSame(DeclaredSignals.NONE, DeclaredSignals.parse((String) null));
        assertSame(DeclaredSignals.NONE, DeclaredSignals.parse("{not json"));
        assertSame(DeclaredSignals.NONE, DeclaredSignals.parse("{\"name\":\"agree\",\"meaning\":\"x\"}"),
                "an object, not a list");
        assertTrue(DeclaredSignals.NONE.isEmpty());
        assertNull(DeclaredSignals.NONE.find("agree"));
    }

    @Test
    void anEntryWithoutANameOrAMeaningIsLeftOut() {
        var signals = DeclaredSignals.parse("""
                [{"name":"agree"},{"meaning":"no name"},{"name":"concern","meaning":"a doubt"}]""");
        assertEquals(List.of("concern"), signals.all().stream().map(DeclaredSignal::name).toList());
        assertSame(DeclaredSignals.NONE, DeclaredSignals.parse("[{\"name\":\"agree\",\"meaning\":\" \"}]"));
    }

    @Test
    void terminalDefaultsToTrueAndAddressedToFalseAndARepeatedNameKeepsTheFirst() {
        var signals = DeclaredSignals.parse("""
                [{"name":"vote","meaning":"first"},{"name":"vote","meaning":"second","terminal":false}]""");
        var vote = signals.find("vote");
        assertEquals("first", vote.meaning());
        assertTrue(vote.terminal());
        assertFalse(vote.addressed());
        assertEquals(SignalEffect.DECLARED, vote.effect(), "a kind outside the runtime's table");
    }
}
