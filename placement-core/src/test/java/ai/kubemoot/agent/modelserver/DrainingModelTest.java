package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * A model the operator is draining before it puts it to sleep takes no new call: it
 * reads as not awake, so placement prefers another awake provider and otherwise the call
 * waits for the switch. A state the reader does not know changes nothing.
 */
class DrainingModelTest {

    private static final String MODEL = "qwen3:14b";

    private static ServerState parse(String modelStates, String loaded) throws Exception {
        String json = "{\"name\":\"vllm-rig1\",\"engine\":\"vllm\",\"endpoint\":\"\",\"ready\":true,"
                + "\"loadedModels\":" + loaded + ",\"modelEndpoints\":{\"" + MODEL + "\":\"http://v:8000\"}"
                + modelStates + "}";
        return ServerSelector.fromJson(new ObjectMapper().readTree(json));
    }

    private static ServerState vllm(String name, Map<String, String> states, boolean loaded) {
        return new ServerState(name, "", 1, 0, 0, loaded ? List.of(MODEL) : List.of(), true,
                "2026-10-19T00:00:00Z", 0L, loaded ? Map.of(MODEL, 0L) : Map.of(), Map.of(), 0L, Map.of(),
                Map.of(), "vllm", Map.of(MODEL, "http://" + name + ":8000"), 0, 0, 0.0, Map.of(), states);
    }

    @Test
    void theDrainingStateRoundTripsThroughThePublishedJson() throws Exception {
        ServerState s = parse(",\"modelStates\":{\"" + MODEL + "\":\"draining\"}", "[\"" + MODEL + "\"]");
        assertTrue(s.isDraining(MODEL));
        assertFalse(s.hasModelLoaded(MODEL), "a draining model is not awake even if still listed as loaded");
        assertTrue(s.hasModel(MODEL), "it stays hosted: a call can wait for the switch and wake it");
    }

    @Test
    void anOperatorThatOmitsTheModelFromLoadedModelsReadsTheSameWay() throws Exception {
        ServerState s = parse(",\"modelStates\":{\"" + MODEL + "\":\"draining\"}", "[]");
        assertFalse(s.hasModelLoaded(MODEL));
        assertTrue(s.isDraining(MODEL));
    }

    @Test
    void aStateTheReaderDoesNotKnowIsNotDrainingAndAbsentStatesAreFine() throws Exception {
        ServerState unknown = parse(",\"modelStates\":{\"" + MODEL + "\":\"frozen\"}", "[\"" + MODEL + "\"]");
        assertFalse(unknown.isDraining(MODEL));
        assertTrue(unknown.hasModelLoaded(MODEL));
        ServerState absent = parse("", "[\"" + MODEL + "\"]");
        assertFalse(absent.isDraining(MODEL));
        assertTrue(absent.modelStates().isEmpty());
        assertFalse(absent.isDraining(null));
    }

    @Test
    void placementSkipsTheDrainingModelForAnotherAwakeProvider() {
        ServerState draining = vllm("vllm-a", Map.of(MODEL, "draining"), true);
        ServerState awake = vllm("vllm-b", Map.of(MODEL, "awake"), true);
        var ranked = ServerSelector.rankCandidates(List.of(draining, awake), MODEL, 9_000L,
                ServerSelector.LoadView.of(p -> 0, p -> 0L, p -> 0.0, n -> false), new StaticFitPredictor());
        assertEquals(2, ranked.size(), "the draining model is still a way to serve the call, by waiting");
        assertEquals("vllm-b", ranked.get(0).server().name());
        assertTrue(ranked.get(0).warm());
        assertFalse(ranked.get(1).warm(), "the draining model is read as asleep, never as warm");
    }

    @Test
    void whenOnlyTheDrainingModelHostsTheCallItIsACandidateThatMustWaitForTheSwitch() {
        ServerState draining = vllm("vllm-a", Map.of(MODEL, "draining"), true);
        var ranked = ServerSelector.rankCandidates(List.of(draining), MODEL, 9_000L,
                ServerSelector.LoadView.of(p -> 0, p -> 0L, p -> 0.0, n -> false), new StaticFitPredictor());
        assertEquals(1, ranked.size());
        assertFalse(ranked.get(0).warm());
    }
}
