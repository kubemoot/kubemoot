package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.HashMap;
import java.util.Map;

/**
 * Directly probes Ollama provider endpoints to fetch model size information,
 * bypassing the NATS KV cache. Used as a fallback when the KV bucket is
 * unavailable (NATS stalled, operator restart window) so the fit-gate remains
 * NATS-INDEPENDENT.
 *
 * <h3>Why this exists</h3>
 * The normal fit-gate resolves a model's cold-load footprint from the NATS KV
 * bucket {@code kubemoot_provider_state}. When NATS is stalled or the operator
 * has just restarted (before its first capacity probe), the KV entries are
 * absent and {@code resolveOccupancyMiB} returns 0 - which causes a static
 * fallback with NO fit check. On 2026-06-14, NATS JetStream stalled under
 * heavy write load, and qwen3:32b spilled onto the 4090 (CPU spill, 91%
 * scenario failure). This class removes that hard NATS dependency.
 *
 * <h3>Resolution order</h3>
 * <ol>
 *   <li>{@code GET <provider>/api/ps} - loaded model sizes from a running
 *       Ollama instance. This is an OBSERVED footprint, already in VRAM, the
 *       most accurate source. Prefer this over the on-disk proxy when the
 *       model is warm on any provider.</li>
 *   <li>{@code GET <provider>/api/tags} - on-disk sizes for all downloaded
 *       models. Applied through {@link ProviderState#ON_DISK_TO_VRAM_FACTOR}
 *       (1.2x inflation) since on-disk underestimates the loaded VRAM size.
 *       Used at true cold start when no provider has the model loaded.</li>
 * </ol>
 *
 * <h3>GraalVM native constraints</h3>
 * Uses Java's built-in {@code java.net.http.HttpClient} (native-safe) and
 * Jackson's {@code readTree} for all JSON navigation (never
 * {@code readValue(.., RecordClass.class)}, which fails silently in native).
 * No new reflection configuration needed.
 *
 * <h3>Fit decision</h3>
 * Uses {@link StaticFitPredictor#usableVramMiB} for the VRAM safety margin -
 * calls the shared static helper rather than re-implementing the math (DRY).
 * A provider whose usable VRAM is unknown (totalVramMiB = 0, no NATS data)
 * is skipped to avoid over-committing on a phantom budget.
 */
@ApplicationScoped
public class OllamaDirectProber {

    private static final Logger log = LoggerFactory.getLogger(OllamaDirectProber.class);

    /** Timeout for direct Ollama API calls. Short: this is a metadata probe, not inference. */
    static final Duration PROBE_TIMEOUT = Duration.ofSeconds(5);

    private final ObjectMapper objectMapper;
    private final HttpClient httpClient;

    @Inject
    public OllamaDirectProber(ObjectMapper objectMapper) {
        this.objectMapper = objectMapper;
        this.httpClient = HttpClient.newBuilder()
                .connectTimeout(PROBE_TIMEOUT)
                .build();
    }

    /**
     * Package-private constructor for tests: accepts a caller-supplied HttpClient
     * so tests can mock HTTP responses without a live Ollama instance.
     */
    OllamaDirectProber(ObjectMapper objectMapper, HttpClient httpClient) {
        this.objectMapper = objectMapper;
        this.httpClient = httpClient;
    }

    /**
     * Result of a direct probe of one provider endpoint. Fields are per-model maps;
     * a model absent from the map means it was not found on that provider.
     *
     * @param loadedSizeMiB  observed VRAM footprint per model name from /api/ps (most accurate)
     * @param onDiskSizeMiB  on-disk size per model name from /api/tags (proxy; inflate by 1.2 for VRAM estimate)
     */
    public record ProbeResult(Map<String, Long> loadedSizeMiB, Map<String, Long> onDiskSizeMiB) {

        static final ProbeResult EMPTY = new ProbeResult(Map.of(), Map.of());

        /**
         * Best footprint estimate for the named model on this provider, in MiB.
         * Prefers an observed loaded size (VRAM-accurate) over the on-disk proxy.
         * Applies the same 1.2x inflation factor as ProviderState.coldLoadFootprintMiB.
         * Returns 0 when the model is absent from both maps.
         */
        public long footprintMiB(String modelName) {
            Long loaded = loadedSizeMiB.get(modelName);
            if (loaded != null && loaded > 0) return loaded;
            Long onDisk = onDiskSizeMiB.get(modelName);
            if (onDisk != null && onDisk > 0) return (long) (onDisk * ProviderState.ON_DISK_TO_VRAM_FACTOR);
            return 0L;
        }

        /** True when the model is currently loaded (warm) on this provider. */
        public boolean isWarm(String modelName) {
            Long loaded = loadedSizeMiB.get(modelName);
            return loaded != null && loaded > 0;
        }
    }

    /**
     * Probe a single provider endpoint. Calls both /api/ps and /api/tags;
     * either endpoint failing independently is tolerated (returns partial data).
     * Returns EMPTY on total failure (provider unreachable).
     *
     * @param providerEndpoint base URL of the Ollama instance, e.g. "http://ollama.ollama-rig1:11434"
     */
    public ProbeResult probe(String providerEndpoint) {
        if (providerEndpoint == null || providerEndpoint.isBlank()) return ProbeResult.EMPTY;
        Map<String, Long> loaded = fetchPs(providerEndpoint);
        Map<String, Long> onDisk = fetchTags(providerEndpoint);
        if (loaded.isEmpty() && onDisk.isEmpty()) return ProbeResult.EMPTY;
        return new ProbeResult(loaded, onDisk);
    }

    /**
     * Given a list of provider states (which carry endpoint URLs), probe all of them
     * and return the best cold-load footprint estimate for the named model across all
     * providers. Prefers a warm observed footprint over the on-disk proxy.
     *
     * This is the NATS-independent fallback: called when {@code resolveOccupancyMiB}
     * finds no footprint in the KV state (all zero). It reads /api/ps + /api/tags
     * directly from each provider's HTTP endpoint and picks the maximum (most
     * conservative) estimate, just as the KV path does.
     *
     * @param states         provider states from NATS KV (may be empty when NATS is down)
     * @param staticEndpoint the agent's own static Ollama endpoint to probe when states empty
     * @param modelName      the model whose footprint is needed
     * @return best MiB estimate, or 0 if all probes fail
     */
    public long resolveFootprintMiB(java.util.List<ProviderState> states,
                                     String staticEndpoint,
                                     String modelName) {
        long[] best = {0L, 0L}; // [0]=bestWarm, [1]=bestDisk
        java.util.Set<String> probed = probeAll(states, modelName, best);
        probeStaticIfNeeded(states, staticEndpoint, modelName, probed, best);
        return footprintFromBest(best[0], best[1]);
    }

    /** Probe all KV-state endpoints, accumulate best warm/disk into best[]. Returns probed set. */
    private java.util.Set<String> probeAll(java.util.List<ProviderState> states,
                                            String modelName, long[] best) {
        java.util.Set<String> probed = new java.util.HashSet<>();
        for (ProviderState ps : states) {
            if (ps.endpoint() == null || ps.endpoint().isBlank()) continue;
            probed.add(ps.endpoint());
            accumulate(probe(ps.endpoint()), modelName, best);
        }
        return probed;
    }

    /** Probe the static endpoint when NATS is down and it has not been probed yet. */
    private void probeStaticIfNeeded(java.util.List<ProviderState> states,
                                      String staticEndpoint, String modelName,
                                      java.util.Set<String> probed, long[] best) {
        if (states.isEmpty() && staticEndpoint != null
                && !staticEndpoint.isBlank() && !probed.contains(staticEndpoint)) {
            accumulate(probe(staticEndpoint), modelName, best);
        }
    }

    /** Accumulate the probe result into best[0]=warm, best[1]=disk. */
    private static void accumulate(ProbeResult r, String modelName, long[] best) {
        long warm = r.loadedSizeMiB().getOrDefault(modelName, 0L);
        long disk = r.onDiskSizeMiB().getOrDefault(modelName, 0L);
        if (warm > best[0]) best[0] = warm;
        if (disk > best[1]) best[1] = disk;
    }

    /** Convert best warm/disk into a single MiB footprint estimate. */
    static long footprintFromBest(long bestWarm, long bestDisk) {
        if (bestWarm > 0L) return bestWarm;
        if (bestDisk > 0L) return (long) (bestDisk * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        return 0L;
    }

    /**
     * Determine which provider endpoint to prefer, based on direct probes.
     * For the fit-gate degraded path: after self-fetching footprint data, pick
     * a provider that (a) fits usable VRAM and (b) has the model warm if possible.
     * Returns null when no provider passes the VRAM fit check (caller falls back
     * to static endpoint and logs loudly - truly-unknown degraded path).
     *
     * @param states         NATS KV provider states (may have stale/zero footprints)
     * @param staticEndpoint the agent's own Ollama endpoint, probed as a warm
     *                       rescue when the KV state is empty (degraded window)
     * @param modelName      target model
     * @param footprintMiB   the resolved footprint (from resolveFootprintMiB)
     * @return preferred endpoint URL, or null if none fit
     */
    public String pickFittingEndpoint(java.util.List<ProviderState> states,
                                       String staticEndpoint,
                                       String modelName,
                                       long footprintMiB) {
        if (footprintMiB <= 0) return null;

        String bestWarm = null;
        String bestCold = null;

        for (ProviderState ps : states) {
            Fit fit = classifyEndpoint(ps, modelName, footprintMiB);
            if (fit == Fit.WARM) {
                if (bestWarm == null) bestWarm = ps.endpoint();
            } else if (fit == Fit.COLD && bestCold == null) {
                bestCold = ps.endpoint();
            }
        }
        if (bestWarm != null) return bestWarm;

        // Degraded window: when the KV state is empty the loop above sees no
        // providers, but the agent's own static endpoint may have the model
        // ALREADY RESIDENT. A warm model is a zero-cost reuse with no spill risk,
        // so credit it directly rather than standing aside on an empty KV. (A COLD
        // static endpoint is NOT admitted here - without provider VRAM data we
        // cannot gate it, and refusing avoids a CPU spill.)
        if (isWarmStaticEndpoint(staticEndpoint, modelName)) {
            return staticEndpoint;
        }
        return bestCold;
    }

    /** True when the static endpoint is set and already has the model resident. */
    private boolean isWarmStaticEndpoint(String staticEndpoint, String modelName) {
        return staticEndpoint != null && !staticEndpoint.isBlank()
                && probe(staticEndpoint).isWarm(modelName);
    }

    /** How a single provider relates to the target model for the fit-gate degraded path. */
    private enum Fit { WARM, COLD, SKIP }

    /**
     * Classify one provider for {@link #pickFittingEndpoint}: WARM when the model is
     * already resident (share it, no cold-load fit check applies), COLD when a cold-load
     * fits both alone and alongside residents (same gate as StaticFitPredictor.predict via
     * the shared coldFitsUsable), SKIP when the provider is not ready or cannot hold a
     * cold-load.
     */
    private Fit classifyEndpoint(ProviderState ps, String modelName, long footprintMiB) {
        if (!ps.ready()) {
            return Fit.SKIP;
        }
        ProbeResult r = probe(ps.endpoint());
        // Warm: the model is already resident here - share it (concurrent
        // requests run off the one loaded copy, no cold-load, so the fit gate
        // does not apply). Strongly preferred over any cold-load.
        if (r.isWarm(modelName)) {
            return Fit.WARM;
        }
        // Cold candidate: must fit ALONE and ALONGSIDE residents so the degraded
        // path can never spill a model onto a card that cannot hold it.
        long usable = StaticFitPredictor.usableVramMiB(ps.totalVramMiB());
        long residentOthers = ps.loadedFootprintSumMiB();
        return StaticFitPredictor.coldFitsUsable(footprintMiB, usable, residentOthers)
                ? Fit.COLD : Fit.SKIP;
    }

    // ---- Private HTTP helpers ----

    /**
     * Fetch loaded model sizes from Ollama /api/ps (models currently in VRAM).
     * Returns a map of model name to size_vram in MiB. Never throws; returns
     * empty map on any failure.
     *
     * Response shape (per Ollama API):
     * {"models":[{"name":"qwen3:32b","size_vram":23380803584,...},...]}
     * size_vram is in bytes.
     */
    Map<String, Long> fetchPs(String endpoint) {
        try {
            var body = getJson(endpoint + "/api/ps");
            if (body == null) return Map.of();
            var result = new HashMap<String, Long>();
            var models = body.path("models");
            if (models.isArray()) {
                for (var m : models) {
                    String name = m.path("name").asText("");
                    long sizeBytes = m.path("size_vram").asLong(0L);
                    if (!name.isEmpty() && sizeBytes > 0) {
                        result.put(name, sizeBytes / (1024L * 1024L));
                    }
                }
            }
            return Map.copyOf(result);
        } catch (Exception e) {
            log.debug("fetchPs failed for {}: {}", endpoint, e.getMessage());
            return Map.of();
        }
    }

    /**
     * Fetch on-disk model sizes from Ollama /api/tags (all downloaded models).
     * Returns a map of model name to size in MiB. Never throws; returns empty
     * map on any failure.
     *
     * Response shape (per Ollama API):
     * {"models":[{"name":"qwen3:32b","size":20200000000,...},...]}
     * size is in bytes.
     */
    Map<String, Long> fetchTags(String endpoint) {
        try {
            var body = getJson(endpoint + "/api/tags");
            if (body == null) return Map.of();
            var result = new HashMap<String, Long>();
            var models = body.path("models");
            if (models.isArray()) {
                for (var m : models) {
                    String name = m.path("name").asText("");
                    long sizeBytes = m.path("size").asLong(0L);
                    if (!name.isEmpty() && sizeBytes > 0) {
                        result.put(name, sizeBytes / (1024L * 1024L));
                    }
                }
            }
            return Map.copyOf(result);
        } catch (Exception e) {
            log.debug("fetchTags failed for {}: {}", endpoint, e.getMessage());
            return Map.of();
        }
    }

    /**
     * Execute a GET request and parse the response body as a JSON tree.
     * Returns null on any HTTP/IO failure; never throws. Native-safe: uses
     * readTree, not readValue(.., RecordClass.class).
     */
    private com.fasterxml.jackson.databind.JsonNode getJson(String url) {
        try {
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(url))
                    .timeout(PROBE_TIMEOUT)
                    .GET()
                    .build();
            var response = httpClient.send(request, HttpResponse.BodyHandlers.ofString());
            if (response.statusCode() != 200) {
                log.debug("GET {} returned HTTP {}", url, response.statusCode());
                return null;
            }
            return objectMapper.readTree(response.body());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return null;
        } catch (Exception e) {
            log.debug("GET {} failed: {}", url, e.getMessage());
            return null;
        }
    }
}
