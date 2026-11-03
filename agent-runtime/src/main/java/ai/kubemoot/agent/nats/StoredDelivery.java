package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.function.Function;
import java.util.function.ToLongFunction;
import java.util.regex.Matcher;

/**
 * The synthesis input for a thread whose tooler results are stored per call, with the results that no
 * analyst read set apart: the call lines of covered results are replaced by one line naming who read
 * them (they are never read from the store for the synthesizer), the coverage lines come before the
 * thread, and only the uncovered results are read, in parallel, to be delivered whole in the room the
 * synthesizer has.
 */
final class StoredDelivery {

    /** Most reads of stored results at once. */
    static final int MAX_PARALLEL_READS = 8;

    private static final String COVERAGE_TEMPLATE = RefinePrompts.load("synthesis-coverage.adl");

    private final EvidenceCoverage coverage;
    private final ThreadCatalog catalog;
    private final String conversation;
    private final List<StoredResult> results;
    private final long readMillis;
    private long catalogMillis;

    private StoredDelivery(EvidenceCoverage coverage, ThreadCatalog catalog, String conversation,
                           List<StoredResult> results, long readMillis) {
        this.coverage = coverage;
        this.catalog = catalog;
        this.conversation = conversation;
        this.results = results;
        this.readMillis = readMillis;
    }

    /**
     * Prepares the delivery. {@code prefix} is the text before the thread (the signal context),
     * {@code thread} the thread text with the manifests' call lines in it, {@code read} returns a stored
     * result's content (null when it cannot be read).
     */
    static StoredDelivery prepare(String prefix, String thread, ThreadCatalog catalog, EvidenceCoverage coverage,
                                  Function<String, String> read) {
        var covered = new LinkedHashMap<String, String>();
        var toRead = new LinkedHashSet<String>();
        Matcher m = CallManifest.CALL_LINE.matcher(thread);
        while (m.find()) {
            String key = m.group(1);
            if (coverage.isCovered(key)) {
                covered.put(key, "[COVERED key=" + key + " tool=" + m.group(2) + " by="
                        + String.join(",", coverage.analystsOf(key)) + "]");
            } else {
                toRead.add(key);
            }
        }
        long start = System.nanoTime();
        Map<String, String> contents = readAll(toRead, read);
        long readMillis = (System.nanoTime() - start) / 1_000_000;
        String body = EvidenceSynthesis.replaceCallLines(thread, covered);
        var results = StoredResult.listFrom(body, contents::get);
        return new StoredDelivery(coverage, catalog, prefix + coverageBlock(catalog, coverage) + body, results,
                readMillis);
    }

    /** The coverage lines and the catalog counts, as the ADL block that heads the thread; empty without a catalog. */
    private static String coverageBlock(ThreadCatalog catalog, EvidenceCoverage coverage) {
        if (catalog.isEmpty()) {
            return "";
        }
        return RefinePrompts.render(COVERAGE_TEMPLATE,
                Map.of("coverage", coverage.summary(), "counts", catalog.counts())) + "\n\n";
    }

    private static Map<String, String> readAll(Set<String> keys, Function<String, String> read) {
        var contents = new LinkedHashMap<String, String>();
        if (keys.isEmpty()) {
            return contents;
        }
        ExecutorService pool = Executors.newFixedThreadPool(Math.min(MAX_PARALLEL_READS, keys.size()));
        try {
            var futures = new LinkedHashMap<String, CompletableFuture<String>>();
            keys.forEach(k -> futures.put(k, CompletableFuture.supplyAsync(() -> read.apply(k), pool)));
            futures.forEach((k, f) -> contents.put(k, f.join()));
        } finally {
            pool.shutdown();
        }
        return contents;
    }

    /** The synthesis input: the coverage block, then the thread with covered call lines replaced. */
    String conversation() {
        return conversation;
    }

    /** The uncovered stored results, in manifest order. */
    List<StoredResult> results() {
        return results;
    }

    EvidenceCoverage coverage() {
        return coverage;
    }

    ThreadCatalog catalog() {
        return catalog;
    }

    long readMillis() {
        return readMillis;
    }

    long catalogMillis() {
        return catalogMillis;
    }

    void setCatalogMillis(long millis) {
        this.catalogMillis = millis;
    }

    /** The tokens the readable uncovered results take, counted with {@code tokens}. */
    long uncoveredTokens(ToLongFunction<String> tokens) {
        return results.stream().filter(StoredResult::readable).mapToLong(r -> tokens.applyAsLong(r.content())).sum();
    }

    List<String> coveredKeys() {
        return coverage.covered().stream().map(ThreadCatalog.Entry::key).toList();
    }

    List<String> uncoveredKeys() {
        return new ArrayList<>(coverage.uncovered().stream().map(ThreadCatalog.Entry::key).toList());
    }
}
