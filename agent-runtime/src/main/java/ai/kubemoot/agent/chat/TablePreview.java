package ai.kubemoot.agent.chat;

import java.util.ArrayList;
import java.util.List;

/**
 * A bounded view of a {@code kubectl} style table that did not fit an agent's window: the header
 * and the leading columns that fit {@link #MAX_WIDTH} characters, for every row up to
 * {@link #MAX_ROWS}, with the row count and the names of the columns left out. A wide table ends in
 * a column such as {@code LABELS} that is thousands of characters a row; cutting the text by
 * length shows a row and a half, while cutting by column shows every row's name and status.
 *
 * <p>It is a projection, never a cut: a row is shown by whole columns or not at all, the columns
 * left out are named, and a table with more rows than are shown says how many there are. Built
 * from the result alone, in code; the stored file is untouched.
 */
final class TablePreview {

    /** Most characters of one row shown; columns are kept whole while they end inside this. */
    static final int MAX_WIDTH = 160;

    /** Most rows shown. */
    static final int MAX_ROWS = 40;

    /** Most characters of the preview, so a table of many wide rows cannot fill the window. */
    static final int MAX_CHARS = 4_000;

    private TablePreview() {}

    /** The preview of {@code text}, or null when it is not a {@code kubectl} table. */
    static String of(String text) {
        if (text == null) {
            return null;
        }
        List<String> lines = new ArrayList<>();
        for (String line : text.split("\n", -1)) {
            if (!line.isBlank()) {
                lines.add(line.stripTrailing());
            }
        }
        if (lines.isEmpty()) {
            return null;
        }
        List<String> titles = ResultType.kubectlTitles(lines.get(0));
        if (titles.isEmpty()) {
            return null;
        }
        return render(lines, titles);
    }

    private static String render(List<String> lines, List<String> titles) {
        String header = lines.get(0);
        int[] starts = starts(header, titles);
        List<String> rows = lines.subList(1, lines.size());
        int kept = keptColumns(starts, widest(lines));
        int width = kept < titles.size() ? starts[kept] : widest(lines);

        StringBuilder out = new StringBuilder();
        out.append(rows.size()).append(rows.size() == 1 ? " row" : " rows")
                .append(". Columns shown: ").append(String.join(", ", titles.subList(0, kept))).append('.');
        if (kept < titles.size()) {
            out.append(" Columns left out: ").append(String.join(", ", titles.subList(kept, titles.size())))
                    .append(" (read them from the stored result with a query).");
        }
        out.append('\n').append(cut(header, width));
        int shown = 0;
        for (String row : rows) {
            String cut = cut(row, width);
            if (shown >= MAX_ROWS || out.length() + cut.length() + 1 > MAX_CHARS) {
                break;
            }
            out.append('\n').append(cut);
            shown++;
        }
        if (shown < rows.size()) {
            out.append("\n(first ").append(shown).append(" of ").append(rows.size()).append(" rows)");
        }
        return out.toString();
    }

    /** The column that starts at {@code starts[i]} is title i; the next starts where its padding ends. */
    private static int[] starts(String header, List<String> titles) {
        int[] starts = new int[titles.size()];
        int from = 0;
        for (int i = 0; i < titles.size(); i++) {
            starts[i] = header.indexOf(titles.get(i), from);
            from = starts[i] + titles.get(i).length();
        }
        return starts;
    }

    /**
     * How many leading columns end within {@link #MAX_WIDTH}; the first column is always kept. A
     * column ends where the next begins; the last column is not padded, so it ends at the longest row.
     */
    private static int keptColumns(int[] starts, int longestRow) {
        int kept = 1;
        while (kept < starts.length && starts[kept] <= MAX_WIDTH) {
            kept++;
        }
        return kept == starts.length && longestRow > MAX_WIDTH && kept > 1 ? kept - 1 : kept;
    }

    private static int widest(List<String> lines) {
        return lines.stream().mapToInt(String::length).max().orElse(0);
    }

    private static String cut(String row, int width) {
        return row.length() <= width ? row : row.substring(0, width).stripTrailing();
    }
}
