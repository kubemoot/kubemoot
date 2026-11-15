package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;

/**
 * What a tool result is, read once: its content type ({@link ResultType}), how many rows, and the
 * names of its columns or fields. The row count is the list length, or the length of the
 * {@code items} list of an object (a Kubernetes list); any other result has an unknown count. The
 * names are a kubectl table's column titles as {@link ColumnNames} writes them (lowercase,
 * underscores), or the top-level keys of the first JSON row as they are; either way the names a
 * query reads them by.
 */
record ToolResultShape(String type, int rows, List<String> columns) {

    ToolResultShape {
        columns = List.copyOf(columns);
    }

    /** A shape with no column names. */
    ToolResultShape(String type, int rows) {
        this(type, rows, List.of());
    }

    boolean json() {
        return ResultType.isJson(type);
    }

    static ToolResultShape of(String result) {
        String text = result == null ? "" : result;
        JsonNode root = ResultType.parseJson(text);
        if (root != null) {
            JsonNode list = root.isArray() ? root : root.path("items");
            return new ToolResultShape(ResultType.JSON, list.isArray() ? list.size() : -1, fieldNames(root, list));
        }
        String type = ResultType.ofNonJson(text);
        return new ToolResultShape(type, -1, ResultType.KUBECTL_TABLE.equals(type) ? tableColumns(text) : List.of());
    }

    /** The names a query gives the columns of a kubectl table, not the titles as printed. */
    private static List<String> tableColumns(String text) {
        String t = ResultType.trim(text);
        int end = t.indexOf('\n');
        return ColumnNames.normalize(ResultType.kubectlTitles((end < 0 ? t : t.substring(0, end)).stripTrailing()));
    }

    /** The keys of the first object of a JSON list, or of the object itself. */
    private static List<String> fieldNames(JsonNode root, JsonNode list) {
        JsonNode row = list.isArray() ? (list.isEmpty() ? null : list.get(0)) : root;
        List<String> names = new ArrayList<>();
        if (row != null && row.isObject()) {
            Iterator<String> it = row.fieldNames();
            it.forEachRemaining(names::add);
        }
        return names;
    }
}
