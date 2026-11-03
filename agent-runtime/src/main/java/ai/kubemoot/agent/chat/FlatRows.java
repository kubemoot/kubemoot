package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.regex.Pattern;

/**
 * The rows and column names a JSON tool result has once a workspace query reads it. artifact-access
 * flattens a JSON result into plain rows ({@code internal/workspace/flatten.go} is the source of truth;
 * both sides read {@code testdata/flatten_cases.json}, so they cannot drift apart):
 * a list of objects is one row per object; an object holding exactly one non-empty list of objects
 * (a wrapper such as {@code {"data":[...]}}) is one row per element, with its other fields repeated on
 * each row; a nested object is columns named {@code parent_child}, four levels deep at most; a
 * Kubernetes List and a Kubernetes object are read as they are. A result whose flattened table would
 * pass 500 columns, or whose repeated wrapper fields would pass the size bound, is read as it came.
 *
 * <p>The row count is the list length, or -1 when the result is one object with no list (the count is
 * unknown to a catalog line). The column names are the union over all rows, in name order, as the query
 * lists them.
 */
final class FlatRows {

    /** Levels of nested objects that become columns. */
    static final int MAX_DEPTH = 4;
    /** Most columns of one table. */
    static final int MAX_COLUMNS = 500;
    private static final long GROWTH = 4;
    private static final long SLACK = 1L << 20;
    private static final long CELL_BYTES = 12;
    private static final String SEP = "_";
    private static final String WRAPPER_PREFIX = "root";
    private static final String ITEMS = "items";
    private static final String VALUE = "value";
    private static final Pattern UNSAFE = Pattern.compile("[^A-Za-z0-9_]");

    /** Row count (-1 when unknown) and column names. */
    record Table(int rows, List<String> columns) {}

    /** The cell names of one row. Names compare without regard to case, as SQL does. */
    private static final class Row {
        private final Set<String> names = new TreeSet<>();
        private final Set<String> lower = new HashSet<>();

        void put(String name) {
            String col = name;
            for (int n = 2; lower.contains(col.toLowerCase(Locale.ROOT)); n++) {
                col = name + SEP + n;
            }
            lower.add(col.toLowerCase(Locale.ROOT));
            names.add(col);
        }

        boolean has(String name) {
            return lower.contains(name.toLowerCase(Locale.ROOT));
        }
    }

    private final long limit;
    private boolean changed;

    private FlatRows(long inputBytes) {
        this.limit = inputBytes * GROWTH + SLACK;
    }

    /**
     * The table of {@code root}, which was parsed from {@code inputBytes} bytes of JSON text. The size
     * bound is an estimate from the sizes of the column names and repeated wrapper fields, the same sum
     * artifact-access makes; it is not the size of the file artifact-access writes.
     */
    static Table of(JsonNode root, long inputBytes) {
        return new FlatRows(inputBytes).table(root);
    }

    private Table table(JsonNode root) {
        if (root.isArray()) {
            return listTable(root);
        }
        if (!root.isObject()) {
            return new Table(-1, List.of());
        }
        if (keepsShape(root)) {
            return plainTable(root);
        }
        List<String> lists = objectLists(root);
        if (lists.size() == 1) {
            return wrapperTable(root, lists.get(0));
        }
        if (!hasNestedObject(root)) {
            return plainTable(root);
        }
        Row row = new Row();
        object("", root, 0, row);
        return finish(List.of(row), -1, root);
    }

    private Table listTable(JsonNode list) {
        if (startsWithKubernetesObject(list) || !anyNested(list)) {
            return plainTable(list);
        }
        return finish(listRows(list, null), list.size(), list);
    }

    private List<Row> listRows(JsonNode list, Row siblings) {
        List<Row> rows = new ArrayList<>();
        for (JsonNode e : list) {
            Row row = new Row();
            if (e.isObject()) {
                object("", e, 0, row);
            } else {
                value(VALUE, e, 0, row);
            }
            if (siblings != null) {
                for (String name : siblings.names) {
                    row.put(row.has(name) ? WRAPPER_PREFIX + SEP + name : name);
                }
            }
            rows.add(row);
        }
        return rows;
    }

    private Table wrapperTable(JsonNode obj, String key) {
        JsonNode elems = obj.get(key);
        if (startsWithKubernetesObject(elems)) {
            return plainTable(obj);
        }
        Row siblings = new Row();
        long repeated = 0;
        for (String k : sortedNames(obj)) {
            if (!k.equals(key)) {
                value(columnName("", k), obj.get(k), 0, siblings);
                repeated += obj.get(k).toString().getBytes(StandardCharsets.UTF_8).length;
            }
        }
        if (repeated * (elems.size() - 1) > limit) {
            return plainTable(obj);
        }
        changed = true;
        return finish(listRows(elems, siblings), elems.size(), obj);
    }

    /** The table of {@code rows}, or {@code source} read as it is when it would pass a bound. */
    private Table finish(List<Row> rows, int count, JsonNode source) {
        if (!changed || rows.isEmpty()) {
            return plainTable(source);
        }
        Map<String, String> alias = new HashMap<>();
        Map<String, String> owner = new HashMap<>();
        Set<String> columns = new TreeSet<>();
        for (Row r : rows) {
            for (String name : r.names) {
                columns.add(alias.computeIfAbsent(name, n -> claim(n, owner)));
            }
            if (columns.size() > MAX_COLUMNS) {
                return plainTable(source);
            }
        }
        long overhead = 0;
        for (String c : columns) {
            overhead += c.getBytes(StandardCharsets.UTF_8).length + CELL_BYTES;
        }
        if (rows.size() * overhead > limit) {
            return plainTable(source);
        }
        return new Table(count, List.copyOf(columns));
    }

    /** The column a new spelling takes: two spellings that differ only by case are renamed name_2, name_3. */
    private static String claim(String name, Map<String, String> owner) {
        String col = name;
        for (int n = 2; owner.containsKey(col.toLowerCase(Locale.ROOT)); n++) {
            col = name + SEP + n;
        }
        owner.put(col.toLowerCase(Locale.ROOT), name);
        return col;
    }

    /** A document read as it is: the keys of its objects are the columns. */
    private static Table plainTable(JsonNode root) {
        JsonNode list = root.isArray() ? root : root.path(ITEMS);
        Set<String> columns = new TreeSet<>();
        if (list.isArray()) {
            for (JsonNode e : list) {
                addKeys(columns, e);
            }
            return new Table(list.size(), List.copyOf(columns));
        }
        addKeys(columns, root);
        return new Table(-1, List.copyOf(columns));
    }

    private static void addKeys(Set<String> columns, JsonNode element) {
        if (element.isObject()) {
            element.fieldNames().forEachRemaining(columns::add);
        } else {
            columns.add(VALUE);
        }
    }

    private void object(String prefix, JsonNode obj, int depth, Row row) {
        for (String k : sortedNames(obj)) {
            value(columnName(prefix, k), obj.get(k), depth, row);
        }
    }

    private void value(String name, JsonNode v, int depth, Row row) {
        if (v.isObject() && v.size() > 0 && depth < MAX_DEPTH) {
            changed = true;
            object(name, v, depth + 1, row);
            return;
        }
        row.put(name);
    }

    private static String columnName(String prefix, String key) {
        String name = UNSAFE.matcher(key).replaceAll(SEP);
        if (name.isEmpty()) {
            name = "col";
        } else if (prefix.isEmpty() && Character.isDigit(name.charAt(0))) {
            name = SEP + name;
        }
        return prefix.isEmpty() ? name : prefix + SEP + name;
    }

    private static List<String> sortedNames(JsonNode obj) {
        Set<String> names = new TreeSet<>();
        obj.fieldNames().forEachRemaining(names::add);
        return new ArrayList<>(names);
    }

    private static List<String> objectLists(JsonNode obj) {
        List<String> keys = new ArrayList<>();
        for (String k : sortedNames(obj)) {
            if (isObjectList(obj.get(k))) {
                keys.add(k);
            }
        }
        return keys;
    }

    private static boolean isObjectList(JsonNode v) {
        if (!v.isArray() || v.isEmpty()) {
            return false;
        }
        for (JsonNode e : v) {
            if (!e.isObject()) {
                return false;
            }
        }
        return true;
    }

    private static boolean anyNested(JsonNode list) {
        for (JsonNode e : list) {
            if (e.isObject() && hasNestedObject(e)) {
                return true;
            }
        }
        return false;
    }

    private static boolean hasNestedObject(JsonNode obj) {
        for (JsonNode v : obj) {
            if (v.isObject() && v.size() > 0) {
                return true;
            }
        }
        return false;
    }

    /** An object with an {@code items} list, or the metadata of a Kubernetes object. */
    private static boolean keepsShape(JsonNode obj) {
        return obj.path(ITEMS).isArray() || isKubernetesObject(obj);
    }

    private static boolean isKubernetesObject(JsonNode obj) {
        JsonNode meta = obj.path("metadata");
        return meta.isObject() && (meta.path("name").isTextual() || obj.path("kind").isTextual());
    }

    private static boolean startsWithKubernetesObject(JsonNode list) {
        return !list.isEmpty() && list.get(0).isObject() && isKubernetesObject(list.get(0));
    }
}
