package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.File;
import java.util.List;
import org.junit.jupiter.api.Test;

class ResultTypeTest {

    @Test
    void sharedFixtureGivesTheSameTypesAsArtifactAccess() throws Exception {
        JsonNode cases = new ObjectMapper().readTree(new File("../test/fixtures/result-types.json")).get("cases");
        assertTrue(cases.size() > 0, "the fixture has cases");
        for (JsonNode c : cases) {
            assertEquals(c.get("type").asText(), ResultType.of(c.get("input").asText()), c.get("name").asText());
        }
    }

    @Test
    void aMissingResultIsText() {
        assertEquals(ResultType.TEXT, ResultType.of(null));
        assertEquals(ResultType.TEXT, ResultType.of(""));
    }

    @Test
    void jsonIsRecognizedByItsWholeText() {
        assertTrue(ResultType.isJson(ResultType.of("[1,2]")));
        assertFalse(ResultType.isJson(ResultType.of("[1,2] tail")));
        assertFalse(ResultType.isJson(ResultType.of("\"just a string\"")));
        assertNotNull(ResultType.parseJson(" {\"a\":1}\n"));
        assertNull(ResultType.parseJson("{\"a\":"));
        assertNull(ResultType.parseJson("plain"));
    }

    @Test
    void kubectlTitlesAreTheUpperCaseCellsOfTheHeaderLine() {
        assertEquals(List.of("NAME", "NOMINATED NODE"), ResultType.kubectlTitles("NAME      NOMINATED NODE"));
        assertTrue(ResultType.kubectlTitles("NAME").isEmpty(), "one column is not a table");
        assertTrue(ResultType.kubectlTitles("NAME   ready").isEmpty(), "a lower case title is not a kubectl one");
        assertTrue(ResultType.kubectlTitles("").isEmpty());
    }

    @Test
    void onlyTheFirstFiveNonBlankLinesDecide() {
        String csvThenGarbage = "a,b\n1,2\n3,4\n5,6\n7,8\nthis line has no commas and is never read\n";
        assertEquals(ResultType.CSV, ResultType.of(csvThenGarbage));
        String sixth = "a,b\n1,2\n3,4\n5,6\n7,8\n9\n";
        assertEquals(ResultType.CSV, ResultType.of(sixth), "the sixth line is never read");
        assertEquals(ResultType.TEXT, ResultType.of("a,b\n1,2\n3\n"), "a short row inside the first five lines");
    }

    @Test
    void aSentenceWithCommasOverSeveralLinesIsText() {
        assertEquals(ResultType.TEXT, ResultType.of("Pods, services and nodes\nwere all healthy, ready, and fine.\n"));
    }
}
