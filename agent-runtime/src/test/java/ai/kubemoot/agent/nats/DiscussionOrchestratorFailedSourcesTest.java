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

    @Test
    void noFailure_addsNothing() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("curator", "ok");
        assertEquals("", DiscussionOrchestrator.failedSourcesNote(state));
    }

    @Test
    void aMissingTool_isNamedAndNothingInternalIsShown() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("curator", "ok");
        state.failureSignals.put("library-search",
                "Tools unavailable: agent library-search declares search_oreilly_content but the MCP tool source"
                        + " does not offer it. http://ocrew-gateway.crew-ocrew.svc:8080 ConnectException");
        state.failedSources.add("search_oreilly_content");

        var note = DiscussionOrchestrator.failedSourcesNote(state);

        assertEquals("\n\n---\nSome sources could not be reached: search_oreilly_content."
                + " This answer does not include what they would have provided.", note);
        assertFalse(note.contains("library-search"), note);
        assertFalse(note.contains("curator"), note);
        assertFalse(note.contains("http"), note);
        assertFalse(note.contains(".svc"), note);
        assertFalse(note.contains("Exception"), note);
    }

    @Test
    void severalSources_areListedInOrder() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("a", "x");
        state.failedSources.add("zeta_tool");
        state.failedSources.add("alpha_tool");

        assertTrue(DiscussionOrchestrator.failedSourcesNote(state)
                .contains("reached: alpha_tool, zeta_tool."));
    }

    @Test
    void aFailureWithoutToolNames_isStillSaid() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("a", "Mulling failed: timeout period of 300s http://x.svc");

        var note = DiscussionOrchestrator.failedSourcesNote(state);

        assertEquals("\n\n---\nSome sources could not be reached."
                + " This answer does not include what they would have provided.", note);
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
        state.failureSignals.put("zeta", "x");
        state.failureSignals.put("alpha", "y");
        state.failedSources.add("search_oreilly_content");

        var context = DiscussionOrchestrator.buildSignalContext(state, 0);

        assertTrue(context.contains("- Failures: 2 (sources not reached: search_oreilly_content)"), context);
        assertFalse(context.contains("alpha"), context);
    }

    @Test
    void theSynthesisInputReportsZeroFailures() {
        var context = DiscussionOrchestrator.buildSignalContext(new DiscussionOrchestrator.ThreadState("t1"), 0);

        assertTrue(context.contains("- Failures: 0\n"), context);
    }
}
