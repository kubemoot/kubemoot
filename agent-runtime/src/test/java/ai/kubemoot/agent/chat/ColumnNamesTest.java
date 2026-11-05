package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ColumnNamesTest {

    private static List<String> strings(JsonNode array) {
        List<String> out = new ArrayList<>();
        array.forEach(n -> out.add(n.asText()));
        return out;
    }

    @Test
    void sharedFixtureGivesTheSameNamesAsArtifactAccess() throws Exception {
        JsonNode cases = new ObjectMapper().readTree(new File("../test/fixtures/column-names.json")).get("cases");
        assertTrue(cases.size() > 0, "the fixture has cases");
        for (JsonNode c : cases) {
            assertEquals(strings(c.get("output")), ColumnNames.normalize(strings(c.get("input"))), c.get("name").asText());
        }
    }

    @Test
    void namesAreSafeAndNormalizingAgainChangesNothing() {
        var once = ColumnNames.normalize(List.of("NODE SELECTOR", "", "9lives", "NAME", "name", "😀😀", "Größe"));
        assertEquals(once, ColumnNames.normalize(once));
        for (String n : once) {
            assertTrue(n.matches("[a-z_][a-z0-9_]*"), n);
            assertFalse(n.isEmpty());
        }
    }
}
