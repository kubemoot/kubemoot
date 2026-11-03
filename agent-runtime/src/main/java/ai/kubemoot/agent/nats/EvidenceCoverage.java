package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.regex.Pattern;

/**
 * Which of a thread's stored tool calls its analysts read, computed in code. The input is the
 * keys and key patterns each analyst's successful tool calls read (see {@code AnalystReads}) and the
 * analysts that answered with data. A pattern counts every stored call it matches, so a query over
 * {@code <prefix>*-get_vm_rrd_data} covers all nine of them.
 *
 * <p>A stored call that is a tool error is never covered: it holds no data, and the synthesis sees it
 * so a failed source stays visible. Reading a call is taken as covering it: a query that read the call
 * may still have used only some of its rows, which this does not look into.
 */
final class EvidenceCoverage {

    private final ThreadCatalog catalog;
    private final Map<String, Set<String>> coveredBy = new LinkedHashMap<>();

    private EvidenceCoverage(ThreadCatalog catalog) {
        this.catalog = catalog;
    }

    /**
     * The coverage of {@code catalog} by the analysts in {@code successful}; reads of any other agent
     * (one that failed, stood aside or answered NO_DATA) count for nothing.
     */
    static EvidenceCoverage compute(ThreadCatalog catalog, Map<String, Set<String>> reads,
                                    Collection<String> successful) {
        var coverage = new EvidenceCoverage(catalog);
        for (String analyst : successful) {
            Set<String> specs = reads.getOrDefault(analyst, Set.of());
            for (ThreadCatalog.Entry entry : catalog.entries()) {
                if (!entry.error() && specs.stream().anyMatch(spec -> matches(spec, entry.key()))) {
                    coverage.coveredBy.computeIfAbsent(entry.key(), k -> new LinkedHashSet<>()).add(analyst);
                }
            }
        }
        return coverage;
    }

    /**
     * Whether a read spec names the key: a key as it is, a directory ending in a slash (every key
     * below it), or a pattern in which {@code *} stands for any characters except a slash.
     */
    static boolean matches(String spec, String key) {
        if (spec == null || spec.isBlank()) {
            return false;
        }
        if (spec.endsWith("/") && !spec.contains("*")) {
            return key.startsWith(spec);
        }
        if (!spec.contains("*")) {
            return spec.equals(key);
        }
        var regex = new StringBuilder();
        for (String piece : spec.split("\\*", -1)) {
            if (regex.length() > 0) {
                regex.append("[^/]*");
            }
            regex.append(Pattern.quote(piece));
        }
        return Pattern.matches(regex.toString(), key);
    }

    boolean isCovered(String key) {
        return coveredBy.containsKey(key);
    }

    /** The analysts that read the call, in the order they were given. */
    Set<String> analystsOf(String key) {
        return coveredBy.getOrDefault(key, Set.of());
    }

    List<ThreadCatalog.Entry> covered() {
        return catalog.entries().stream().filter(e -> isCovered(e.key())).toList();
    }

    List<ThreadCatalog.Entry> uncovered() {
        return catalog.entries().stream().filter(e -> !isCovered(e.key())).toList();
    }

    /**
     * The coverage as lines for the synthesis: one per analyst that read something, naming for each
     * tool how many of its stored calls were read, then one line for what no analyst read.
     */
    String summary() {
        var perAnalyst = new TreeMap<String, Map<String, Integer>>();
        coveredBy.forEach((key, analysts) -> {
            String tool = toolOf(key);
            analysts.forEach(a -> perAnalyst.computeIfAbsent(a, x -> new TreeMap<>()).merge(tool, 1, Integer::sum));
        });
        var totals = totalsPerTool();
        var lines = new ArrayList<String>();
        perAnalyst.forEach((analyst, tools) -> lines.add("[COVERAGE analyst=" + analyst + " read="
                + tools.values().stream().mapToInt(Integer::intValue).sum() + " of " + catalog.calls()
                + " stored calls: " + ofTotals(tools, totals) + "]"));
        lines.add(uncoveredLine());
        return String.join("\n", lines);
    }

    private String uncoveredLine() {
        var left = uncovered();
        if (left.isEmpty()) {
            return "[COVERAGE read by no analyst: none]";
        }
        var perTool = new TreeMap<String, Integer>();
        left.forEach(e -> perTool.merge(e.tool(), 1, Integer::sum));
        return "[COVERAGE read by no analyst: " + left.size() + " calls (" + perTool + "), "
                + left.stream().mapToLong(ThreadCatalog.Entry::bytes).sum() + " bytes]";
    }

    private static String ofTotals(Map<String, Integer> read, Map<String, Integer> totals) {
        var parts = new ArrayList<String>();
        read.forEach((tool, n) -> parts.add(tool + " " + n + " of " + totals.get(tool)));
        return String.join(", ", parts);
    }

    private Map<String, Integer> totalsPerTool() {
        var totals = new TreeMap<String, Integer>();
        catalog.entries().forEach(e -> totals.merge(e.tool(), 1, Integer::sum));
        return totals;
    }

    private String toolOf(String key) {
        return catalog.entries().stream().filter(e -> e.key().equals(key)).map(ThreadCatalog.Entry::tool)
                .findFirst().orElse("");
    }
}
