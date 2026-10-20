package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ModelUse;
import org.junit.jupiter.api.Test;

import java.util.Arrays;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ModelAttributionTest {

    private static final String PREFERRED = "model-large";
    private static final String ALTERNATIVE = "model-medium";

    @Test
    void oneCall_namesItsModelAndListsItAlone() {
        var meta = ModelAttribution.of(List.of(new ModelUse(ALTERNATIVE, "server-a")));

        assertEquals(ALTERNATIVE, meta.get("modelName"));
        assertEquals(List.of("model-medium@server-a"), meta.get("modelsUsed"));
    }

    @Test
    void callsInTwoPlaces_nameTheLastAndListEachDistinctPlaceInOrder() {
        var meta = ModelAttribution.of(List.of(
                new ModelUse(PREFERRED, "server-a"),
                new ModelUse(ALTERNATIVE, "server-b"),
                new ModelUse(ALTERNATIVE, "server-b")));

        assertEquals(ALTERNATIVE, meta.get("modelName"));
        assertEquals(List.of("model-large@server-a", "model-medium@server-b"), meta.get("modelsUsed"));
        assertEquals("server-b", ModelAttribution.gpuLabel(List.of(
                new ModelUse(PREFERRED, "server-a"), new ModelUse(ALTERNATIVE, "server-b")), "static"));
    }

    @Test
    void theSameModelOnTwoServers_isListedOncePerServer() {
        var meta = ModelAttribution.of(List.of(
                new ModelUse(ALTERNATIVE, "server-a"), new ModelUse(ALTERNATIVE, "server-b")));

        assertEquals(List.of("model-medium@server-a", "model-medium@server-b"), meta.get("modelsUsed"));
    }

    @Test
    void aStaticEndpointCall_namesTheModelAlone_andKeepsTheFallbackLabel() {
        var use = List.of(new ModelUse(PREFERRED, ""));

        assertEquals(List.of(PREFERRED), ModelAttribution.of(use).get("modelsUsed"));
        assertEquals("endpoint-label", ModelAttribution.gpuLabel(use, "endpoint-label"));
    }

    @Test
    void noKnownCall_addsNothing_andKeepsTheFallbackLabel() {
        assertTrue(ModelAttribution.of(null).isEmpty());
        assertTrue(ModelAttribution.of(List.of()).isEmpty());
        assertTrue(ModelAttribution.of(List.of(new ModelUse(null, "server-a"))).isEmpty(),
                "a call with no model name is not attributed");
        assertEquals("fallback", ModelAttribution.gpuLabel(null, "fallback"));
        assertEquals("fallback", ModelAttribution.gpuLabel(List.of(), "fallback"));
    }

    @Test
    void callsWithoutAModel_areSkipped_andTheLastKnownCallIsNamed() {
        var uses = Arrays.asList(new ModelUse(PREFERRED, "server-a"), new ModelUse("", "server-b"));

        assertEquals(PREFERRED, ModelAttribution.of(uses).get("modelName"));
        assertEquals("server-a", ModelAttribution.gpuLabel(uses, "fallback"));
    }

    @Test
    void chatResult_derivesOneCallFromItsModelAndServer_andCopiesItsList() {
        var one = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", ALTERNATIVE, null, 1, 1,
                "server-a", "", List.of());
        assertEquals(List.of(new ModelUse(ALTERNATIVE, "server-a")), one.modelsUsed());
        assertEquals(new ModelUse(ALTERNATIVE, "server-a"), one.lastModelUse());

        var noModel = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", null, null, 1, 1,
                "server-a", "", null);
        assertTrue(noModel.modelsUsed().isEmpty(), "no model, no call listed");
        assertTrue(noModel.evicted().isEmpty(), "null evictions become empty");
        assertEquals(new ModelUse("", "server-a"), noModel.lastModelUse());

        var calls = new java.util.ArrayList<>(List.of(new ModelUse(PREFERRED, "server-a")));
        var listed = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", PREFERRED, null, 1, 1,
                "server-a", "", List.of(), calls);
        calls.add(new ModelUse(ALTERNATIVE, "server-b"));
        assertEquals(1, listed.modelsUsed().size(), "the result keeps its own copy");

        var nullList = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", PREFERRED, null, 1, 1,
                "server-a", "", List.of(), null);
        assertTrue(nullList.modelsUsed().isEmpty());
    }

    @Test
    void modelUse_labelsWithAndWithoutAServer_andTreatsNullAsEmpty() {
        assertEquals("m@p", new ModelUse("m", "p").label());
        assertEquals("m", new ModelUse("m", null).label());
        assertEquals("", new ModelUse(null, null).model());
    }
}
