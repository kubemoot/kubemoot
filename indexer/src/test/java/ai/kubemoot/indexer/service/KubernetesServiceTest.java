package ai.kubemoot.indexer.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class KubernetesServiceTest {

    private final ObjectMapper mapper = new ObjectMapper();

    @Test
    void annotationPatchSetsOnlyTheGivenAnnotations() throws Exception {
        Map<String, String> annotations = new LinkedHashMap<>();
        annotations.put("kubemoot.ai/status", "indexed");
        annotations.put("kubemoot.ai/document-count", "23");
        annotations.put("kubemoot.ai/chunk-count", "110");

        JsonNode patch = mapper.readTree(KubernetesService.annotationPatch(annotations));

        assertEquals(1, patch.size(), "only metadata is patched");
        JsonNode set = patch.path("metadata").path("annotations");
        assertEquals(3, set.size());
        assertEquals("indexed", set.path("kubemoot.ai/status").asText());
        assertEquals("23", set.path("kubemoot.ai/document-count").asText());
        assertEquals("110", set.path("kubemoot.ai/chunk-count").asText());
    }

    @Test
    void annotationPatchEscapesValues() throws Exception {
        String topics = "lists, \"dicts\", back\\slash";
        JsonNode patch = mapper.readTree(KubernetesService.annotationPatch(Map.of("kubemoot.ai/topics", topics)));
        assertEquals(topics, patch.path("metadata").path("annotations").path("kubemoot.ai/topics").asText());
    }

    @Test
    void annotationPatchOfNothingIsAnEmptyAnnotationsObject() throws Exception {
        JsonNode set = mapper.readTree(KubernetesService.annotationPatch(Map.of())).path("metadata").path("annotations");
        assertTrue(set.isObject());
        assertEquals(0, set.size());
    }
}
