package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;
import java.util.regex.Pattern;

/**
 * What a tool result is, read once: its format, how many rows, and the names of its columns or
 * fields. The format is {@code json} when the result is a JSON object or list that parses,
 * {@code text-table} when it is a kubectl style table (a first line of upper-case column titles
 * separated by two or more spaces), and {@code text} for anything else. The row count is the list
 * length, or the length of the {@code items} list of an object (a Kubernetes list); a text result
 * has an unknown count. The names are a table's column titles as {@link ColumnNames} writes them
 * (lowercase, underscores), or the top-level keys of the first JSON row as they are; either way
 * the names a query reads them by.
 *
 * <p>Tables are recognized by the rule the workspace query uses (artifact-access
 * {@code internal/workspace/format.go}, {@code isKubectlTable}, is the source of truth), so the manifest and
 * the query agree on what a table is.
 */
record ToolResultShape(String format, int rows, List<String> columns) {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    /** One column title: words joined by single spaces; titles are separated by two or more spaces. */
    private static final Pattern HEADER_CELL = Pattern.compile("\\S+(?: \\S+)*");
    private static final Pattern HEADER_TITLE = Pattern.compile("^[A-Z][A-Z0-9 _/().-]*$");

    ToolResultShape {
        columns = List.copyOf(columns);
    }

    /** A shape with no column names. */
    ToolResultShape(String format, int rows) {
        this(format, rows, List.of());
    }

    boolean json() {
        return CallHeaders.FORMAT_JSON.equals(format);
    }

    static ToolResultShape of(String result) {
        String t = result == null ? "" : result.strip();
        if (!t.startsWith("[") && !t.startsWith("{")) {
            List<String> titles = tableTitles(t);
            return titles.isEmpty() ? new ToolResultShape(CallHeaders.FORMAT_TEXT, -1)
                    : new ToolResultShape(CallHeaders.FORMAT_TEXT_TABLE, -1, titles);
        }
        try {
            JsonNode root = MAPPER.readTree(t);
            JsonNode list = root.isArray() ? root : root.path("items");
            return new ToolResultShape(CallHeaders.FORMAT_JSON, list.isArray() ? list.size() : -1, fieldNames(root, list));
        } catch (Exception e) {
            return new ToolResultShape(CallHeaders.FORMAT_TEXT, -1);
        }
    }

    /**
     * The column names of a table's header line, or none when the first line is not one. They are the
     * names the workspace query gives the columns ({@link ColumnNames}), not the titles as printed.
     */
    private static List<String> tableTitles(String text) {
        int end = text.indexOf('\n');
        String first = (end < 0 ? text : text.substring(0, end)).stripTrailing();
        List<String> titles = new ArrayList<>();
        var m = HEADER_CELL.matcher(first);
        while (m.find()) {
            if (!HEADER_TITLE.matcher(m.group()).matches()) {
                return List.of();
            }
            titles.add(m.group());
        }
        return titles.size() < 2 ? List.of() : ColumnNames.normalize(titles);
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
