package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SubcommitteeLineTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String RESUMES = """
            [{"name":"k8s-namespaces","description":"Lists and describes namespaces. Also reads quotas."},
             {"name":"k8s-workloads","description":"Reports workloads per namespace."},
             {"name":"bare"}]""";

    @Test
    void reasonIsTheFirstSentenceOfTheDescription() {
        var members = SubcommitteeLine.members(MAPPER, List.of("k8s-namespaces", "k8s-workloads"), RESUMES);
        assertEquals("Lists and describes namespaces", members.get(0).reason());
        assertEquals("Reports workloads per namespace", members.get(1).reason());
    }

    @Test
    void contentNamesEveryAgentWithItsReason() {
        var members = SubcommitteeLine.members(MAPPER, List.of("k8s-namespaces", "k8s-workloads"), RESUMES);
        assertEquals("Convened 2 agents: k8s-namespaces (Lists and describes namespaces), "
                + "k8s-workloads (Reports workloads per namespace)", SubcommitteeLine.content(members));
    }

    @Test
    void singleAgentUsesTheSingularAndAnAgentWithoutDescriptionHasNoParentheses() {
        var members = SubcommitteeLine.members(MAPPER, List.of("bare"), RESUMES);
        assertEquals("Convened 1 agent: bare", SubcommitteeLine.content(members));
    }

    @Test
    void unknownAgentsMissingResumesAndGarbageGiveEmptyReasons() {
        assertEquals("", SubcommitteeLine.members(MAPPER, List.of("ghost"), RESUMES).get(0).reason());
        assertEquals("", SubcommitteeLine.members(MAPPER, List.of("a"), null).get(0).reason());
        assertEquals("", SubcommitteeLine.members(MAPPER, List.of("a"), "not json").get(0).reason());
        assertEquals("", SubcommitteeLine.members(MAPPER, List.of("a"), "{\"name\":\"a\"}").get(0).reason());
    }

    @Test
    void blankNamesAreSkipped() {
        var names = new java.util.ArrayList<String>();
        names.add(null);
        names.add(" ");
        names.add("bare");
        assertEquals(1, SubcommitteeLine.members(MAPPER, names, RESUMES).size());
    }

    @Test
    void longReasonsAreCutShort() {
        String reason = SubcommitteeLine.shortReason("x".repeat(300));
        assertEquals(120, reason.length());
        assertTrue(reason.endsWith("..."));
        assertEquals("", SubcommitteeLine.shortReason(null));
    }

    @Test
    void metadataCarriesNameAndReason() {
        var members = SubcommitteeLine.members(MAPPER, List.of("k8s-workloads"), RESUMES);
        var node = MAPPER.valueToTree(SubcommitteeLine.metadata(members));
        assertEquals("k8s-workloads", node.path("convened").path(0).path("name").asText());
        assertEquals("Reports workloads per namespace", node.path("convened").path(0).path("reason").asText());
    }
}
