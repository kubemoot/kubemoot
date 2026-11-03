package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.Set;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class AnalystReadoutsTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void readKeysComeFromTheSignalsMetadata() throws Exception {
        var signal = MAPPER.readTree("{\"metadata\":{\"readKeys\":[\"a/b\",\"c/*\",\"\",7]}}");

        assertEquals(Set.of("a/b", "c/*"), AnalystReadouts.readKeysOf(signal));
    }

    @Test
    void aSignalWithoutReadKeysHasNone() throws Exception {
        assertTrue(AnalystReadouts.readKeysOf(MAPPER.readTree("{}")).isEmpty());
        assertTrue(AnalystReadouts.readKeysOf(MAPPER.readTree("{\"metadata\":{\"readKeys\":\"x\"}}")).isEmpty());
    }

    @Test
    void onlyTheNoDataSentinelIsNotAnAnswerWithData() {
        assertFalse(AnalystReadouts.answeredWithData("NO_DATA"));
        assertFalse(AnalystReadouts.answeredWithData("  TOOL_GAP \n"));
        assertFalse(AnalystReadouts.answeredWithData("<think>NO_DATA? no, 3</think>NO_DATA"));
        assertFalse(AnalystReadouts.answeredWithData(""));
        assertFalse(AnalystReadouts.answeredWithData(null));
        assertTrue(AnalystReadouts.answeredWithData("NO_DATA was not the case: 3 pods"));
        assertTrue(AnalystReadouts.answeredWithData("<think>NO_DATA</think>3 pods"));
    }
}
