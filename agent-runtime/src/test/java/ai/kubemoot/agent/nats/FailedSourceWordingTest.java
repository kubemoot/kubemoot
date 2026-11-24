package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

class FailedSourceWordingTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static String phraseOf(String summary) {
        ObjectNode resume = MAPPER.createObjectNode().put("triageSummary", summary);
        return FailedSourceWording.phrase(resume);
    }

    @Test
    void phraseStopsAtTheFirstSentenceAndDropsTrailingPunctuation() {
        assertEquals("Reads node health", phraseOf("Reads node health. Also more text."));
        assertEquals("Reads node health", phraseOf("Reads node health ;,: ."));
    }

    @Test
    void phraseFallsBackToTheDescription() {
        ObjectNode resume = MAPPER.createObjectNode().put("description", "Plans the work.");
        assertEquals("Plans the work", FailedSourceWording.phrase(resume));
    }

    @Test
    void phraseOfOnlyPunctuationAndSpacesIsEmpty() {
        assertEquals("", phraseOf(" . , ; : "));
    }

    @Test
    void phraseReadsAVeryLongRunOfTrailingPunctuationQuickly() {
        String text = "Reads node health" + ".;, ".repeat(200_000) + "x".repeat(10);
        String phrase = FailedSourceWording.phrase(MAPPER.createObjectNode().put("triageSummary", text));
        assertTrue(phrase.length() <= FailedSourceWording.MAX_PHRASE_CHARS + 3, phrase);
        assertEquals("Reads node health", phraseOf("Reads node health" + ".;, ".repeat(200_000)));
    }
}
