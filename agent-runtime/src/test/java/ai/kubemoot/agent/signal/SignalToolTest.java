package ai.kubemoot.agent.signal;

import dev.langchain4j.model.chat.request.json.JsonEnumSchema;
import dev.langchain4j.model.chat.request.json.JsonObjectSchema;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SignalToolTest {

    private final SignalTool tool = new SignalTool(SignalTestKinds.consent3());

    @Test
    void theSchemaIsGeneratedFromTheArchetype() {
        var spec = tool.spec();
        assertEquals("signal", spec.name());
        JsonObjectSchema params = spec.parameters();
        var kind = (JsonEnumSchema) params.properties().get("kind");
        assertEquals(List.of("agree", "concern", "question", "stand_aside", "tool_gap", "no_data", "block"),
                kind.enumValues(), "the kinds are the archetype's names, in its order");
        assertTrue(spec.description().contains("- question: ask the agent that made a finding to recheck it"),
                "each kind's meaning is in the description: " + spec.description());
        assertEquals(List.of("kind", "statement"), params.required());
        assertTrue(params.properties().keySet().containsAll(List.of("to", "statement", "reason", "evidence")));
        assertTrue(params.properties().get("to").description().contains("question"),
                "to says which kinds need it");
    }

    @Test
    void anArchetypeWithoutAnAddressedKindOffersNoTo() {
        var plain = new SignalTool(DeclaredSignals.parse("[{\"name\":\"approve\",\"meaning\":\"yes\"}]"));
        var params = plain.spec().parameters();
        assertFalse(params.properties().containsKey("to"));
        assertEquals(List.of("approve"), ((JsonEnumSchema) params.properties().get("kind")).enumValues());
    }

    @Test
    void aValidCallIsTheVerdict() {
        var outcome = tool.record("""
                {"kind":"concern","statement":"k8s-nodes reported 100 nodes","reason":"the node list has 7",
                 "evidence":"ns/crew/t1/k8s-nodes/001-resources_list"}""");
        assertTrue(outcome.recorded());
        var call = outcome.call();
        assertEquals("concern", call.kind());
        assertEquals(SignalEffect.CONCERN, call.effect());
        assertEquals("", call.to(), "to is kept only for an addressed kind");
        assertEquals("k8s-nodes reported 100 nodes\nReason: the node list has 7\n"
                + "Evidence: ns/crew/t1/k8s-nodes/001-resources_list", call.stated());
        assertEquals("{\"recorded\":\"concern\"}", outcome.response());
    }

    @Test
    void aQuestionNamesTheAgentItIsSaidTo() {
        var outcome = tool.record("""
                {"kind":"question","to":"k8s-nodes","statement":"can you recheck the node count?",
                 "reason":"100 seems high for this homelab"}""");
        assertTrue(outcome.recorded());
        assertEquals("k8s-nodes", outcome.call().to());
        assertEquals(SignalEffect.QUESTION, outcome.call().effect());
        assertTrue(outcome.response().contains("\"to\":\"k8s-nodes\""), outcome.response());
    }

    @Test
    void anAddressedKindWithoutToIsRefusedSoTheModelCanFixIt() {
        var outcome = tool.record("{\"kind\":\"question\",\"statement\":\"recheck?\"}");
        assertFalse(outcome.recorded());
        assertNull(outcome.call());
        assertTrue(outcome.response().contains("\"error\""));
        assertTrue(outcome.response().contains("to"));
    }

    @Test
    void anUndeclaredKindOrUnreadableArgumentsAreRefused() {
        var unknown = tool.record("{\"kind\":\"vote\",\"statement\":\"x\"}");
        assertFalse(unknown.recorded());
        assertTrue(unknown.response().contains("agree, concern, question"), unknown.response());
        assertFalse(tool.record("not json").recorded());
        assertFalse(tool.record(null).recorded());
        assertFalse(tool.record("").recorded());
    }

    @Test
    void aLongArgumentIsCut() {
        var outcome = tool.record("{\"kind\":\"agree\",\"statement\":\"" + "x".repeat(5000) + "\"}");
        assertEquals(SignalTool.MAX_ARGUMENT_CHARS, outcome.call().statement().length());
    }

    @Test
    void anAddressedConcernMayNameTheAgentItDoubtsAndStaysANoteWithout() {
        var named = new SignalTool(SignalTestKinds.consent3ConcernRecheck());
        var withTo = named.record("{\"kind\":\"concern\",\"to\":\"k8s-nodes\",\"statement\":\"100 nodes?\"}");
        assertTrue(withTo.recorded());
        assertEquals("k8s-nodes", withTo.call().to());
        var without = named.record("{\"kind\":\"concern\",\"statement\":\"100 nodes?\"}");
        assertTrue(without.recorded(), "a concern naming no agent is a note on the table");
        assertEquals("", without.call().to());
        var to = named.spec().parameters().properties().get("to").description();
        assertTrue(to.contains("Required for: question."), to);
        assertTrue(to.contains("For concern, name the agent whose claim you doubt"), to);
        assertTrue(named.record("{\"kind\":\"question\",\"statement\":\"recheck?\"}").response().contains("error"),
                "a question still needs to");
    }
}
