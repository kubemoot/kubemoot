package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import org.junit.jupiter.api.Test;

/**
 * The row count and column names of a JSON tool result must be the ones artifact-access builds when a
 * query reads it. Both read the same cases file, which lists the table the query service makes for each
 * document ({@code artifact-access/internal/workspace/testdata/flatten_cases.json}).
 */
class FlatRowsTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Path CASES =
            Path.of("..", "artifact-access", "internal", "workspace", "testdata", "flatten_cases.json");

    @Test
    void everyCaseOfTheQueryServiceHasTheSameRowsAndColumns() throws IOException {
        JsonNode cases = MAPPER.readTree(Files.readString(CASES));
        assertTrue(cases.size() > 10, "the cases file must be found and not empty");
        List<String> failures = new ArrayList<>();
        for (JsonNode c : cases) {
            String input = c.get("input").asText();
            FlatRows.Table got = FlatRows.of(MAPPER.readTree(input), input.getBytes(java.nio.charset.StandardCharsets.UTF_8).length);
            // A document that is one object with no list is one table row, and a catalog line has no count for it.
            int rows = c.has("runtimeRows") ? c.get("runtimeRows").asInt() : c.get("rows").asInt();
            List<String> columns = new ArrayList<>();
            c.get("columns").forEach(n -> columns.add(n.asText()));
            if (got.rows() != rows || !got.columns().equals(columns)) {
                failures.add(c.get("name").asText() + ": got " + got.rows() + " " + got.columns()
                        + ", want " + rows + " " + columns);
            }
        }
        assertEquals(List.of(), failures);
    }

    @Test
    void aWrapperObjectCountsItsListAsRowsInTheCatalog() {
        ToolResultShape shape = ToolResultShape.of("{\"data\":[{\"time\":1,\"cpu\":0.5},{\"time\":2,\"cpu\":0.7}]}");
        assertEquals(new ToolResultShape("json", 2, List.of("cpu", "time")), shape);
    }

    @Test
    void nestedObjectsAreCatalogColumnsJoinedWithUnderscores() {
        ToolResultShape shape = ToolResultShape.of("{\"memory\":{\"total\":8,\"used\":2},\"uptime\":5}");
        assertEquals(new ToolResultShape("json", -1, List.of("memory_total", "memory_used", "uptime")), shape);
    }

    @Test
    void aKubernetesListKeepsItsNestedFieldNames() {
        ToolResultShape shape = ToolResultShape.of("{\"items\":[{\"metadata\":{\"name\":\"a\"},\"spec\":{}}]}");
        assertEquals(new ToolResultShape("json", 1, List.of("metadata", "spec")), shape);
    }

    @Test
    void rowsWithDifferentKeysListEveryColumn() {
        ToolResultShape shape = ToolResultShape.of("[{\"a\":1},{\"b\":2}]");
        assertEquals(new ToolResultShape("json", 2, List.of("a", "b")), shape);
    }

    @Test
    void aWrapperFieldRepeatedPastTheSizeBoundIsReadAsItCame() {
        StringBuilder b = new StringBuilder("{\"data\":[");
        for (int i = 0; i < 5000; i++) {
            b.append(i > 0 ? "," : "").append("{\"t\":").append(i).append('}');
        }
        b.append("],\"blob\":\"").append("x".repeat(2000)).append("\"}");
        ToolResultShape shape = ToolResultShape.of(b.toString());
        assertEquals(new ToolResultShape("json", -1, List.of("blob", "data")), shape);
    }

    @Test
    void aHugeWrapperWithNothingRepeatedIsFlattened() {
        StringBuilder b = new StringBuilder("{\"data\":[");
        for (int i = 0; i < 50_000; i++) {
            b.append(i > 0 ? "," : "").append("{\"t\":").append(i).append(",\"cpu\":0.5}");
        }
        b.append("]}");
        assertEquals(new ToolResultShape("json", 50_000, List.of("cpu", "t")), ToolResultShape.of(b.toString()));
    }

    @Test
    void unexpectedJsonStillGivesAShape() {
        assertEquals(new ToolResultShape("json", 0, List.of()), ToolResultShape.of("[]"));
        assertEquals(new ToolResultShape("json", -1, List.of("data")), ToolResultShape.of("{\"data\":[1,2]}"));
        assertEquals(new ToolResultShape("text", -1), ToolResultShape.of("{\"data\":["));
    }
}
