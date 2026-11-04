package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class SynthesisFilesTest {

    @Test
    void aThreadThatDescribesAFileGetsTheRules() {
        String rules = SynthesisFiles.rulesFor("[Response (a)]\n[CALL key=ns/c/t/a/001-x tool=x args={} bytes=1]");

        assertTrue(rules.startsWith("DEFINE DOMAIN synthesis-files"), rules);
        assertTrue(rules.endsWith("\n\n"), "a blank line separates the rules from the thread");
        assertTrue(rules.contains("UNKNOWN"));
    }

    @Test
    void aNotStoredLineGetsTheRulesToo() {
        assertFalse(SynthesisFiles.rulesFor("[NOT STORED tool=x args={} error=false]").isEmpty());
    }

    @Test
    void aThreadWithNoFileDescriptionGetsNothing() {
        assertEquals("", SynthesisFiles.rulesFor("[Response (a)]\n12 pods"));
        assertEquals("", SynthesisFiles.rulesFor(""));
        assertEquals("", SynthesisFiles.rulesFor(null));
    }

    @Test
    void theRulesNeverTellTheSynthesizerToOpenAFile() {
        String rules = SynthesisFiles.rulesFor("[CALL key=k tool=t]");

        assertFalse(rules.contains("workspace_query"), "the synthesizer has no tools");
        assertTrue(rules.contains("NEVER derive a count"), rules);
    }
}
