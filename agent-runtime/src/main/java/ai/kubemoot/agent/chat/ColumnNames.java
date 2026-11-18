package ai.kubemoot.agent.chat;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * The names a table's columns are shown and queried by: ASCII lowercase letters, digits and
 * underscore, so SQL needs no quoting ({@code NODE SELECTOR} is {@code node_selector}). The
 * same rule runs in workspace-access ({@code internal/colname}); both test suites run the cases
 * of {@code test/fixtures/column-names.json}, so the description in the discussion and the
 * table a query reads cannot drift apart.
 *
 * <p>A run of other characters is one underscore, none at either end. A name that ends up empty
 * is {@code col<position>} (positions start at 1); one that starts with a digit gets {@code c_}.
 * Names that collide get {@code _2}, {@code _3}.
 */
final class ColumnNames {

    /** Most names a column list shows by default; the same number as artifact-access's {@code colname.MaxListed}. */
    static final int MAX_LISTED = 60;
    /** Most characters of one name in a list; a longer name is cut and marked with "...". */
    static final int MAX_NAME_CHARS = 60;

    private ColumnNames() {
    }

    /**
     * The column list of a [CALL ...] line: the names joined by commas, at most {@link #MAX_LISTED}
     * of them, then {@code ,+<n>more} for the rest. Square brackets become parentheses so a name
     * cannot end the line early. The same rule runs in artifact-access ({@code colname.List}) and
     * its no such column error; both test suites run {@code test/fixtures/column-list.json}.
     */
    static String list(List<String> names) {
        return list(names, MAX_LISTED);
    }

    /** {@link #list(List)} with its own limit, for a manifest too crowded to list every column. */
    static String list(List<String> names, int limit) {
        int shown = Math.max(0, Math.min(names.size(), limit));
        List<String> parts = new ArrayList<>(shown + 1);
        for (String name : names.subList(0, shown)) {
            String one = name.replace('[', '(').replace(']', ')');
            parts.add(one.codePointCount(0, one.length()) <= MAX_NAME_CHARS ? one
                    : one.substring(0, one.offsetByCodePoints(0, MAX_NAME_CHARS)) + "...");
        }
        if (names.size() > shown) {
            parts.add("+" + (names.size() - shown) + "more");
        }
        return String.join(",", parts);
    }

    /** The safe name of every header, in order. */
    static List<String> normalize(List<String> headers) {
        List<String> out = new ArrayList<>(headers.size());
        Set<String> taken = new HashSet<>();
        for (int i = 0; i < headers.size(); i++) {
            String base = fallback(clean(headers.get(i)), i);
            String name = base;
            for (int n = 2; taken.contains(name); n++) {
                name = base + "_" + n;
            }
            taken.add(name);
            out.add(name);
        }
        return out;
    }

    /** One header cut down to lowercase ASCII letters, digits and single underscores; may be empty. */
    static String clean(String header) {
        StringBuilder b = new StringBuilder();
        boolean separator = false;
        for (int i = 0; i < header.length(); i++) {
            char c = header.charAt(i);
            if (c >= 'A' && c <= 'Z') {
                c = (char) (c + ('a' - 'A'));
            } else if (!(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9')) {
                separator = b.length() > 0;
                continue;
            }
            if (separator) {
                b.append('_');
                separator = false;
            }
            b.append(c);
        }
        return b.toString();
    }

    private static String fallback(String name, int index) {
        if (name.isEmpty()) {
            return "col" + (index + 1);
        }
        return Character.isDigit(name.charAt(0)) ? "c_" + name : name;
    }
}
