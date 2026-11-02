package ai.kubemoot.agent.nats;

import java.util.Map;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class RefinePromptsTest {

    @Test
    void placesAreFilledInOnePassSoInsertedTextIsNeverReadAsAPlace() {
        assertEquals("a {{y}} b $1 c {{z}}",
                RefinePrompts.render("a {{x}} b {{y}} c {{z}}", Map.of("x", "{{y}}", "y", "$1")));
    }

    @Test
    void dollarSignsAndBackslashesInTheTextSurvive() {
        assertEquals("v \\1 $0 \\\\ end", RefinePrompts.render("v {{a}} end", Map.of("a", "\\1 $0 \\\\")));
    }

    @Test
    void aTemplateWithNoPlacesAndAnEmptyMapIsUnchanged() {
        assertEquals("plain", RefinePrompts.render("plain", Map.of()));
    }

    @Test
    void theFirstRoundHasNoNotesSectionAndLaterRoundsDo() {
        String first = RefinePrompts.round(1, "CTX", null, "B1");
        String later = RefinePrompts.round(3, "CTX", "- [k] 7", "B3");

        assertTrue(first.contains("CTX") && first.contains("B1") && first.contains("round 1"), first);
        assertFalse(first.contains("WORKING NOTES SO FAR"), first);
        assertTrue(later.contains("WORKING NOTES SO FAR:\n- [k] 7") && later.contains("B3") && later.contains("round 3"), later);
        assertFalse(first.contains("{{") || later.contains("{{"));
    }

    @Test
    void theSystemPromptStatesTheRulesAndTheNotesFormat() {
        String system = RefinePrompts.system();

        assertTrue(system.contains("NEVER round a number") && system.contains("NEVER merge two distinct items"), system);
        assertTrue(system.contains("- [<call key>]") && system.contains("part i of n"), system);
        assertTrue(system.contains("analyst"), system);
    }

    @Test
    void theFinalSectionCarriesTheNotes() {
        String section = RefinePrompts.finalNotes("- [k] 7 pods");

        assertTrue(section.contains("WORKING NOTES FROM THE STORED TOOL RESULTS") && section.endsWith("- [k] 7 pods"), section);
    }
}
