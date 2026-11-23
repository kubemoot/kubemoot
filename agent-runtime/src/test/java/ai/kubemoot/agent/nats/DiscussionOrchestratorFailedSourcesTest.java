package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The closing note an answer carries when an agent reported failure: user terms only. */
class DiscussionOrchestratorFailedSourcesTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static com.fasterxml.jackson.databind.JsonNode failureWith(String failedTool) throws Exception {
        return MAPPER.readTree("{\"metadata\":{\"failedTool\":\"" + failedTool + "\"}}");
    }

    private static final String RESUMES = "["
            + "{\"name\":\"library-search\",\"description\":\"Searches the O'Reilly catalog for books, videos"
            + " and courses. Uses several queries.\"},"
            + "{\"name\":\"curator\",\"description\":\"Ranks catalog results\","
            + "\"triageSummary\":\"Ranks results by recency and depth\"},"
            + "{\"name\":\"bare\",\"description\":\"\"}]";

    @Test
    void noFailure_addsNothing() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("curator", "ok");
        assertEquals("", DiscussionOrchestrator.failedSourcesNote(state, RESUMES));
    }

    @Test
    void aFailedAgent_isDescribedByItsDeclaredDescriptionAndNothingInternalIsShown() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("curator", "ok");
        state.failureSignals.put("library-search",
                "Tools unavailable: agent library-search declares search_oreilly_content but the MCP tool source"
                        + " does not offer it. http://ocrew-gateway.crew-ocrew.svc:8080 ConnectException");
        state.failedSources.add("search_oreilly_content");

        var note = DiscussionOrchestrator.failedSourcesNote(state, RESUMES);

        assertEquals("\n\n---\nSome sources could not be reached (affected: Searches the O'Reilly catalog"
                + " for books, videos and courses). This answer does not include what they would have provided.",
                note);
        assertFalse(note.contains("library-search"), note);
        assertFalse(note.contains("search_oreilly_content"), note);
        assertFalse(note.contains("curator"), note);
        assertFalse(note.contains("http"), note);
        assertFalse(note.contains("Exception"), note);
    }

    @Test
    void severalAgents_areListedInOrder_preferringTheTriageSummary() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("library-search", "x");
        state.failureSignals.put("curator", "y");

        assertTrue(DiscussionOrchestrator.failedSourcesNote(state, RESUMES)
                .contains("(affected: Ranks results by recency and depth; Searches the O'Reilly catalog"));
    }

    @Test
    void anAgentWithoutADescriptionOrResume_isStillSaid() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("bare", "Mulling failed: timeout period of 300s http://x.svc");
        state.failureSignals.put("unknown-agent", "boom");
        String plain = "\n\n---\nSome sources could not be reached."
                + " This answer does not include what they would have provided.";

        assertEquals(plain, DiscussionOrchestrator.failedSourcesNote(state, RESUMES));
        assertEquals(plain, DiscussionOrchestrator.failedSourcesNote(state, null));
        assertEquals(plain, DiscussionOrchestrator.failedSourcesNote(state, "not json {"));
        assertEquals(plain, DiscussionOrchestrator.failedSourcesNote(state, "{\"name\":\"bare\"}"));
    }

    @Test
    void aLongDescription_isCutAtAWordBoundary() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("long", "x");
        String resumes = "[{\"name\":\"long\",\"description\":\"" + "word ".repeat(60) + "\"}]";

        var phrases = FailedSourceWording.phrases(resumes, state.failureSignals.keySet());

        assertEquals(1, phrases.size());
        assertTrue(phrases.get(0).endsWith("word..."), phrases.get(0));
        assertTrue(phrases.get(0).length() <= FailedSourceWording.MAX_PHRASE_CHARS + 3, phrases.get(0));
    }

    @Test
    void failedToolNames_comeFromTheSignalMetadata() throws Exception {
        assertEquals(List.of("get_nodes", "get_events"),
                DiscussionOrchestrator.failedSourceNames(failureWith("get_nodes,get_events")));
    }

    @Test
    void failedToolNames_dropAnythingThatIsNotAToolName() throws Exception {
        assertEquals(List.of("ok_tool"), DiscussionOrchestrator.failedSourceNames(
                failureWith("ok_tool, http://x.svc:80, has space, ocrew-gateway.crew-ocrew.svc")));
    }

    @Test
    void noMetadata_givesNoNames() throws Exception {
        assertTrue(DiscussionOrchestrator.failedSourceNames(MAPPER.readTree("{}")).isEmpty());
        assertTrue(DiscussionOrchestrator.failedSourceNames(null).isEmpty());
    }

    @Test
    void theSynthesisInputCountsFailuresAndNamesTheSources() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("library-search", "x");
        state.failureSignals.put("alpha", "y");
        state.failedSources.add("search_oreilly_content");

        var context = DiscussionOrchestrator.buildSignalContext(state, 0, RESUMES);

        assertTrue(context.contains("- Failures: 2 (sources not reached: Searches the O'Reilly catalog"), context);
        assertFalse(context.contains("search_oreilly_content"), context);
        assertFalse(context.contains("library-search"), context);
    }

    @Test
    void theSynthesisInputReportsZeroFailures() {
        var context = DiscussionOrchestrator.buildSignalContext(new DiscussionOrchestrator.ThreadState("t1"), 0, RESUMES);

        assertTrue(context.contains("- Failures: 0\n"), context);
    }
}
