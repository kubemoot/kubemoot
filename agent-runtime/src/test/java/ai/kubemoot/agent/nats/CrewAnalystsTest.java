package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class CrewAnalystsTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void onlyNamedAnalystsAreListedInResumeOrder() throws Exception {
        var resumes = MAPPER.readTree("""
                [{"name":"k8s","role":"tooler"},{"name":"b-analyst","role":"analyst"},
                 {"role":"analyst"},{"name":"","role":"analyst"},{"name":"a-analyst","role":"analyst"}]""");

        var names = CrewAnalysts.analysts(resumes).stream().map(n -> n.path("name").asText()).toList();

        assertEquals(java.util.List.of("b-analyst", "a-analyst"), names);
    }

    @Test
    void nothingThatIsNotAListOfResumesIsAnAnalyst() throws Exception {
        assertTrue(CrewAnalysts.analysts(null).isEmpty());
        assertTrue(CrewAnalysts.analysts(MAPPER.readTree("{\"name\":\"x\",\"role\":\"analyst\"}")).isEmpty());
        assertTrue(CrewAnalysts.analysts(MAPPER.readTree("[]")).isEmpty());
    }
}
