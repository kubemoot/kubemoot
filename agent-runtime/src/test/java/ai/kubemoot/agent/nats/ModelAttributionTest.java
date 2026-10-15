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
        var meta = ModelAttribution.of(List.of(new ModelUse(ALTERNATIVE, "provider-a")));

        assertEquals(ALTERNATIVE, meta.get("modelName"));
        assertEquals(List.of("model-medium@provider-a"), meta.get("modelsUsed"));
    }

    @Test
    void callsInTwoPlaces_nameTheLastAndListEachDistinctPlaceInOrder() {
        var meta = ModelAttribution.of(List.of(
                new ModelUse(PREFERRED, "provider-a"),
                new ModelUse(ALTERNATIVE, "provider-b"),
                new ModelUse(ALTERNATIVE, "provider-b")));

        assertEquals(ALTERNATIVE, meta.get("modelName"));
        assertEquals(List.of("model-large@provider-a", "model-medium@provider-b"), meta.get("modelsUsed"));
        assertEquals("provider-b", ModelAttribution.gpuLabel(List.of(
                new ModelUse(PREFERRED, "provider-a"), new ModelUse(ALTERNATIVE, "provider-b")), "static"));
    }

    @Test
    void theSameModelOnTwoProviders_isListedOncePerProvider() {
        var meta = ModelAttribution.of(List.of(
                new ModelUse(ALTERNATIVE, "provider-a"), new ModelUse(ALTERNATIVE, "provider-b")));

        assertEquals(List.of("model-medium@provider-a", "model-medium@provider-b"), meta.get("modelsUsed"));
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
        assertTrue(ModelAttribution.of(List.of(new ModelUse(null, "provider-a"))).isEmpty(),
                "a call with no model name is not attributed");
        assertEquals("fallback", ModelAttribution.gpuLabel(null, "fallback"));
        assertEquals("fallback", ModelAttribution.gpuLabel(List.of(), "fallback"));
    }

    @Test
    void callsWithoutAModel_areSkipped_andTheLastKnownCallIsNamed() {
        var uses = Arrays.asList(new ModelUse(PREFERRED, "provider-a"), new ModelUse("", "provider-b"));

        assertEquals(PREFERRED, ModelAttribution.of(uses).get("modelName"));
        assertEquals("provider-a", ModelAttribution.gpuLabel(uses, "fallback"));
    }

    @Test
    void chatResult_derivesOneCallFromItsModelAndProvider_andCopiesItsList() {
        var one = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", ALTERNATIVE, null, 1, 1,
                "provider-a", "", List.of());
        assertEquals(List.of(new ModelUse(ALTERNATIVE, "provider-a")), one.modelsUsed());
        assertEquals(new ModelUse(ALTERNATIVE, "provider-a"), one.lastModelUse());

        var noModel = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", null, null, 1, 1,
                "provider-a", "", null);
        assertTrue(noModel.modelsUsed().isEmpty(), "no model, no call listed");
        assertTrue(noModel.evicted().isEmpty(), "null evictions become empty");
        assertEquals(new ModelUse("", "provider-a"), noModel.lastModelUse());

        var calls = new java.util.ArrayList<>(List.of(new ModelUse(PREFERRED, "provider-a")));
        var listed = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", PREFERRED, null, 1, 1,
                "provider-a", "", List.of(), calls);
        calls.add(new ModelUse(ALTERNATIVE, "provider-b"));
        assertEquals(1, listed.modelsUsed().size(), "the result keeps its own copy");

        var nullList = new ai.kubemoot.agent.chat.ChatService.ChatResult("c", "r", PREFERRED, null, 1, 1,
                "provider-a", "", List.of(), null);
        assertTrue(nullList.modelsUsed().isEmpty());
    }

    @Test
    void modelUse_labelsWithAndWithoutAProvider_andTreatsNullAsEmpty() {
        assertEquals("m@p", new ModelUse("m", "p").label());
        assertEquals("m", new ModelUse("m", null).label());
        assertEquals("", new ModelUse(null, null).model());
    }
}
