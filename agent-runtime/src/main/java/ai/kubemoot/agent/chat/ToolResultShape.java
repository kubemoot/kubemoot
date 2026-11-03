package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Pattern;

/**
 * What a tool result is, read once: its format, how many rows, and the names of its columns or
 * fields. The format is {@code json} when the result is a JSON object or list that parses,
 * {@code text-table} when it is a kubectl style table (a first line of upper-case column titles
 * separated by two or more spaces), and {@code text} for anything else. A text result
 * has an unknown count. The names are a table's column titles, or the columns a query reads a JSON
 * result by, and the row count is the number of rows that query sees (see {@link FlatRows}): the list
 * length, the length of the {@code items} list, or the list of a wrapper object such as
 * {@code {"data":[...]}}.
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
            FlatRows.Table table = FlatRows.of(MAPPER.readTree(t), t.getBytes(java.nio.charset.StandardCharsets.UTF_8).length);
            return new ToolResultShape(CallHeaders.FORMAT_JSON, table.rows(), table.columns());
        } catch (Exception e) {
            return new ToolResultShape(CallHeaders.FORMAT_TEXT, -1);
        }
    }

    /** The column titles of a table's header line, or none when the first line is not one. */
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
        return titles.size() < 2 ? List.of() : titles;
    }
}
