package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

/**
 * Unit tests for {@link OllamaDirectProber} - the NATS-independent fallback that
 * self-fetches /api/tags and /api/ps directly from Ollama endpoints when the NATS
 * KV bucket is absent or stalled.
 *
 * All HTTP calls are mocked; no live Ollama required (plain JUnit, not @QuarkusTest).
 * Tests validate the exact 2026-06-14 failure scenario: NATS stalled, KV returns
 * no footprints, 32b must still be refused on the 4090 and admitted on the 5090.
 *
 * Reuses the existing provider-test VRAM constants (5090 = 32605 MiB, 4090 = 24563 MiB)
 * for consistency with the 101 ProviderSelectorTest / StaticFitPredictorTest fixtures.
 */
@SuppressWarnings("unchecked")
class OllamaDirectProberTest {

    private static final long VRAM_5090 = 32_605L;
    private static final long VRAM_4090 = 24_563L;

    // Realistic on-disk sizes and VRAM sizes from the homelab (same values used in
    // the 2026-06-11/14 incident notes and the ProviderState.coldLoadFootprintMiB tests).
    private static final long DISK_32B_MIB  = 19_265L;  // qwen3:32b on-disk (bytes -> MiB after /1024/1024)
    private static final long VRAM_32B_MIB  = 22_281L;  // qwen3:32b observed loaded VRAM
    private static final long DISK_8B_MIB   =  4_863L;  // qwen3:8b on-disk
    private static final long DISK_14B_MIB  =  8_149L;  // qwen3:14b on-disk

    // ---- helpers ----

    private static ObjectMapper mapper() {
        return new ObjectMapper();
    }

    /** Build a mock HttpClient that returns the given body with HTTP 200 for any request. */
    @SuppressWarnings("rawtypes")
    private static HttpClient mockHttp(String body) throws Exception {
        var client = mock(HttpClient.class);
        var response = mock(HttpResponse.class);
        when(response.statusCode()).thenReturn(200);
        when(response.body()).thenReturn(body);
        when(client.send(any(HttpRequest.class), any())).thenReturn(response);
        return client;
    }

    /** Build a mock HttpClient that throws IOException for any request. */
    private static HttpClient unreachableHttp() throws Exception {
        var client = mock(HttpClient.class);
        when(client.send(any(HttpRequest.class), any()))
                .thenThrow(new java.io.IOException("connection refused"));
        return client;
    }

    /**
     * Build a mock HttpClient that returns different bodies for /api/ps vs /api/tags.
     * Uses argument captor on the URI to distinguish the two endpoint paths.
     */
    @SuppressWarnings("rawtypes")
    private static HttpClient mockHttpTwoEndpoints(String psBody, String tagsBody) throws Exception {
        var client = mock(HttpClient.class);
        var psResponse = mock(HttpResponse.class);
        when(psResponse.statusCode()).thenReturn(200);
        when(psResponse.body()).thenReturn(psBody);
        var tagsResponse = mock(HttpResponse.class);
        when(tagsResponse.statusCode()).thenReturn(200);
        when(tagsResponse.body()).thenReturn(tagsBody);
        when(client.send(any(HttpRequest.class), any())).thenAnswer(inv -> {
            HttpRequest req = inv.getArgument(0);
            return req.uri().toString().contains("/api/ps") ? psResponse : tagsResponse;
        });
        return client;
    }

    /** /api/tags JSON for a provider with qwen3:32b and qwen3:8b on disk. */
    private static String tagsJson(long disk32bBytes, long disk8bBytes) {
        return """
                {"models":[
                  {"name":"qwen3:32b","size":%d},
                  {"name":"qwen3:8b","size":%d}
                ]}
                """.formatted(disk32bBytes, disk8bBytes);
    }

    /** /api/ps JSON with the given model loaded in VRAM (bytes). */
    private static String psJson(String model, long sizeVramBytes) {
        return """
                {"models":[{"name":"%s","size_vram":%d}]}
                """.formatted(model, sizeVramBytes);
    }

    /** Empty /api/ps - nothing loaded. */
    private static String emptyPs() { return "{\"models\":[]}"; }

    /** Build a ProviderState with known VRAM and no loaded footprints (cold start). */
    private static ProviderState coldState(String name, String endpoint, long totalVramMiB) {
        return new ProviderState(name, endpoint, 1, 0, 0,
                List.of(), true, "2026-06-14T00:00:00Z", totalVramMiB,
                Map.of(), Map.of());
    }

    // ---- fetchPs ----

    @Test
    void fetchPs_parsesLoadedVramSize() throws Exception {
        long expectedBytes = VRAM_32B_MIB * 1024L * 1024L;
        var http = mockHttp(psJson("qwen3:32b", expectedBytes));
        var prober = new OllamaDirectProber(mapper(), http);

        var result = prober.fetchPs("http://rig0:11434");

        assertEquals(VRAM_32B_MIB, result.get("qwen3:32b"),
                "loaded VRAM size in MiB must match size_vram / 1024 / 1024");
    }

    @Test
    void fetchPs_emptyModels_returnsEmptyMap() throws Exception {
        var http = mockHttp(emptyPs());
        var prober = new OllamaDirectProber(mapper(), http);

        assertTrue(prober.fetchPs("http://rig1:11434").isEmpty());
    }

    @Test
    void fetchPs_unreachable_returnsEmptyMap() throws Exception {
        var http = unreachableHttp();
        var prober = new OllamaDirectProber(mapper(), http);

        assertTrue(prober.fetchPs("http://rig1:11434").isEmpty(),
                "unreachable /api/ps must degrade gracefully to empty, not throw");
    }

    // ---- fetchTags ----

    @Test
    void fetchTags_parsesOnDiskSize() throws Exception {
        long expectedBytes = DISK_32B_MIB * 1024L * 1024L;
        var http = mockHttp(tagsJson(expectedBytes, DISK_8B_MIB * 1024L * 1024L));
        var prober = new OllamaDirectProber(mapper(), http);

        var result = prober.fetchTags("http://rig0:11434");

        assertEquals(DISK_32B_MIB, result.get("qwen3:32b"),
                "on-disk size in MiB must match size / 1024 / 1024");
        assertEquals(DISK_8B_MIB, result.get("qwen3:8b"));
    }

    @Test
    void fetchTags_unreachable_returnsEmptyMap() throws Exception {
        var http = unreachableHttp();
        var prober = new OllamaDirectProber(mapper(), http);

        assertTrue(prober.fetchTags("http://rig1:11434").isEmpty());
    }

    // ---- ProbeResult.footprintMiB ----

    @Test
    void probeResult_prefersLoadedOverOnDisk() {
        // When the model is warm on this provider, use the observed VRAM size, not
        // the on-disk proxy. The loaded size is the most accurate source.
        var r = new OllamaDirectProber.ProbeResult(
                Map.of("qwen3:32b", VRAM_32B_MIB),
                Map.of("qwen3:32b", DISK_32B_MIB));
        assertEquals(VRAM_32B_MIB, r.footprintMiB("qwen3:32b"),
                "loaded (VRAM) footprint must beat the on-disk proxy");
    }

    @Test
    void probeResult_fallsBackToOnDiskWithInflation() {
        // Model not loaded: fall back to on-disk size inflated by ON_DISK_TO_VRAM_FACTOR.
        var r = new OllamaDirectProber.ProbeResult(
                Map.of(),
                Map.of("qwen3:32b", DISK_32B_MIB));
        long expected = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        assertEquals(expected, r.footprintMiB("qwen3:32b"),
                "on-disk fallback must apply the 1.2x inflation factor");
    }

    @Test
    void probeResult_missingModel_returnsZero() {
        var r = new OllamaDirectProber.ProbeResult(Map.of(), Map.of());
        assertEquals(0L, r.footprintMiB("qwen3:32b"));
    }

    @Test
    void probeResult_isWarm_trueWhenLoaded() {
        var r = new OllamaDirectProber.ProbeResult(
                Map.of("qwen3:32b", VRAM_32B_MIB), Map.of());
        assertTrue(r.isWarm("qwen3:32b"));
        assertFalse(r.isWarm("qwen3:8b"));
    }

    // ---- footprintFromBest (static helper) ----

    @Test
    void footprintFromBest_prefersWarm() {
        long result = OllamaDirectProber.footprintFromBest(VRAM_32B_MIB, DISK_32B_MIB);
        assertEquals(VRAM_32B_MIB, result,
                "warm footprint takes priority (no inflation - it is an observed VRAM size)");
    }

    @Test
    void footprintFromBest_usesDiskWithInflation_whenNoWarm() {
        long result = OllamaDirectProber.footprintFromBest(0L, DISK_32B_MIB);
        assertEquals((long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR), result);
    }

    @Test
    void footprintFromBest_returnsZero_whenBothAbsent() {
        assertEquals(0L, OllamaDirectProber.footprintFromBest(0L, 0L));
    }

    // ---- resolveFootprintMiB - the primary NATS-independent fallback ----

    /**
     * The 2026-06-14 scenario: NATS stalled (KV returns empty), /api/tags reachable.
     * The prober fetches the on-disk size from both providers and returns the
     * inflated footprint. The fit-gate can then refuse 32b on the 4090 and admit
     * it on the 5090 - exactly what was broken.
     */
    @Test
    void resolveFootprintMiB_kvAbsent_usesTagsAsFallback() throws Exception {
        // Both providers have 32b and 8b on disk; neither has 32b loaded.
        long disk32Bytes = DISK_32B_MIB * 1024L * 1024L;
        long disk8Bytes  = DISK_8B_MIB  * 1024L * 1024L;
        var http = mockHttpTwoEndpoints(emptyPs(), tagsJson(disk32Bytes, disk8Bytes));
        var prober = new OllamaDirectProber(mapper(), http);

        // States: two providers (NATS data might have empty footprints - simulating KV-absent).
        var rig05090 = coldState("ollama-gpu",  "http://rig0:11434", VRAM_5090);
        var rig14090 = coldState("ollama-rig1", "http://rig1:11434", VRAM_4090);
        var states = List.of(rig05090, rig14090);

        long footprint32b = prober.resolveFootprintMiB(states, "http://rig0:11434", "qwen3:32b");
        long footprint8b  = prober.resolveFootprintMiB(states, "http://rig0:11434", "qwen3:8b");

        // 32b footprint: inflated on-disk size.
        long expected32b = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        assertEquals(expected32b, footprint32b,
                "32b footprint from /api/tags must be inflated (KV absent)");

        // 8b footprint: inflated on-disk size.
        long expected8b = (long) (DISK_8B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        assertEquals(expected8b, footprint8b,
                "8b footprint from /api/tags must be inflated (KV absent)");

        // Gate check: 32b must exceed 4090 usable VRAM (the bug was it didn't).
        long usable4090 = StaticFitPredictor.usableVramMiB(VRAM_4090);
        assertTrue(footprint32b > usable4090,
                "self-fetched 32b footprint must exceed 4090 usable VRAM (" + usable4090 + ") "
                        + "so the fit-gate refuses it: footprint=" + footprint32b);

        // And 32b must fit the 5090.
        long usable5090 = StaticFitPredictor.usableVramMiB(VRAM_5090);
        assertTrue(footprint32b <= usable5090,
                "32b must still fit the 5090 usable VRAM (" + usable5090 + "): footprint=" + footprint32b);

        // And 8b fits both.
        assertTrue(footprint8b <= usable4090, "8b fits 4090: " + footprint8b + " <= " + usable4090);
        assertTrue(footprint8b <= usable5090, "8b fits 5090");
    }

    /**
     * When a peer provider has the model warm, the observed VRAM footprint is
     * preferred over the on-disk proxy. This is more accurate and avoids the
     * 1.2x inflation being applied to an already-accurate measurement.
     */
    @Test
    void resolveFootprintMiB_prefersWarmObservedFootprint() throws Exception {
        // rig0 has qwen3:32b warm (loaded in VRAM); rig1 only has it on disk.
        long vramBytes = VRAM_32B_MIB * 1024L * 1024L;
        long diskBytes = DISK_32B_MIB * 1024L * 1024L;

        // Two providers with different responses: rig0 has ps warm, rig1 has tags only.
        var http = mock(HttpClient.class);
        var rig0PsResponse   = stubResponse(psJson("qwen3:32b", vramBytes));
        var rig0TagsResponse = stubResponse(tagsJson(diskBytes, DISK_8B_MIB * 1024L * 1024L));
        var rig1PsResponse   = stubResponse(emptyPs());
        var rig1TagsResponse = stubResponse(tagsJson(diskBytes, DISK_8B_MIB * 1024L * 1024L));
        when(http.send(any(HttpRequest.class), any())).thenAnswer(inv -> {
            HttpRequest req = inv.getArgument(0);
            String uri = req.uri().toString();
            if (uri.contains("rig0") && uri.contains("/api/ps"))   return rig0PsResponse;
            if (uri.contains("rig0") && uri.contains("/api/tags")) return rig0TagsResponse;
            if (uri.contains("rig1") && uri.contains("/api/ps"))   return rig1PsResponse;
            return rig1TagsResponse;
        });

        var prober = new OllamaDirectProber(mapper(), http);
        var rig0 = coldState("ollama-gpu",  "http://rig0:11434", VRAM_5090);
        var rig1 = coldState("ollama-rig1", "http://rig1:11434", VRAM_4090);

        long fp = prober.resolveFootprintMiB(List.of(rig0, rig1), "http://rig0:11434", "qwen3:32b");

        assertEquals(VRAM_32B_MIB, fp,
                "observed warm footprint (VRAM_32B_MIB) must beat on-disk proxy (no inflation)");
    }

    /**
     * Both probes unreachable (provider down): resolveFootprintMiB returns 0 so the
     * caller's final fallback (model-name estimate or static endpoint) can take over.
     */
    @Test
    void resolveFootprintMiB_bothProbesUnreachable_returnsZero() throws Exception {
        var http = unreachableHttp();
        var prober = new OllamaDirectProber(mapper(), http);
        var rig0 = coldState("ollama-gpu", "http://rig0:11434", VRAM_5090);

        long fp = prober.resolveFootprintMiB(List.of(rig0), "http://rig0:11434", "qwen3:32b");

        assertEquals(0L, fp, "all probes failed -> 0, let caller handle static fallback");
    }

    /**
     * NATS completely down (states empty): prober falls back to probing the static
     * endpoint directly and still extracts the footprint.
     */
    @Test
    void resolveFootprintMiB_natsDown_probesStaticEndpoint() throws Exception {
        long diskBytes = DISK_32B_MIB * 1024L * 1024L;
        var http = mockHttpTwoEndpoints(emptyPs(), tagsJson(diskBytes, 0L));
        var prober = new OllamaDirectProber(mapper(), http);

        // Empty states = NATS down; static endpoint is the agent's own Ollama.
        long fp = prober.resolveFootprintMiB(List.of(), "http://rig0:11434", "qwen3:32b");

        long expected = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        assertEquals(expected, fp,
                "when NATS is down (states empty), static endpoint is probed as the last resort");
    }

    /**
     * 8b and 14b fit both the 4090 and 5090 when resolved from /api/tags.
     * Validates that the self-fetch does not incorrectly refuse small models.
     */
    @Test
    void resolveFootprintMiB_smallModels_fitBothGpus() throws Exception {
        long disk8Bytes  = DISK_8B_MIB  * 1024L * 1024L;
        long disk14Bytes = DISK_14B_MIB * 1024L * 1024L;
        // tags returns both 8b and 14b; ps returns nothing loaded.
        var tagsBody = """
                {"models":[
                  {"name":"qwen3:8b","size":%d},
                  {"name":"qwen3:14b","size":%d}
                ]}""".formatted(disk8Bytes, disk14Bytes);
        var http = mockHttpTwoEndpoints(emptyPs(), tagsBody);
        var prober = new OllamaDirectProber(mapper(), http);
        var rig0 = coldState("ollama-gpu", "http://rig0:11434", VRAM_5090);

        long fp8b  = prober.resolveFootprintMiB(List.of(rig0), "http://rig0:11434", "qwen3:8b");
        long fp14b = prober.resolveFootprintMiB(List.of(rig0), "http://rig0:11434", "qwen3:14b");

        long usable4090 = StaticFitPredictor.usableVramMiB(VRAM_4090);
        assertTrue(fp8b  <= usable4090, "8b must fit 4090 via self-fetch: " + fp8b  + " <= " + usable4090);
        assertTrue(fp14b <= usable4090, "14b must fit 4090 via self-fetch: " + fp14b + " <= " + usable4090);
    }

    // ---- pickFittingEndpoint ----

    @Test
    void pickFittingEndpoint_prefersWarmProvider() throws Exception {
        // rig0 has qwen3:32b warm; rig1 does not. Both fit the 5090.
        // pickFittingEndpoint should prefer the warm one.
        long vramBytes = VRAM_32B_MIB * 1024L * 1024L;
        long diskBytes = DISK_32B_MIB * 1024L * 1024L;
        long fp32b = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);

        var http = mock(HttpClient.class);
        var rig0PsResponse   = stubResponse(psJson("qwen3:32b", vramBytes));
        var rig0TagsResponse = stubResponse(tagsJson(diskBytes, 0L));
        var rig1PsResponse   = stubResponse(emptyPs());
        var rig1TagsResponse = stubResponse(tagsJson(diskBytes, 0L));
        when(http.send(any(HttpRequest.class), any())).thenAnswer(inv -> {
            HttpRequest req = inv.getArgument(0);
            String uri = req.uri().toString();
            if (uri.contains("rig0") && uri.contains("/api/ps"))   return rig0PsResponse;
            if (uri.contains("rig0") && uri.contains("/api/tags")) return rig0TagsResponse;
            if (uri.contains("rig1") && uri.contains("/api/ps"))   return rig1PsResponse;
            return rig1TagsResponse;
        });

        var prober = new OllamaDirectProber(mapper(), http);
        // Two 5090s for simplicity - both fit a 32b model.
        var rig0 = coldState("ollama-gpu",  "http://rig0:11434", VRAM_5090);
        var rig1 = coldState("ollama-rig1", "http://rig1:11434", VRAM_5090);

        String picked = prober.pickFittingEndpoint(List.of(rig0, rig1), null, "qwen3:32b", fp32b);

        assertEquals("http://rig0:11434", picked,
                "warm provider (rig0 has 32b loaded) must be preferred over cold rig1");
    }

    @Test
    void pickFittingEndpoint_refusesProviderWhenFootprintExceedsUsableVram() throws Exception {
        // Only the 4090 is in the states list; 32b does not fit it.
        long fp32b = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        var http = mockHttp(emptyPs()); // response doesn't matter - VRAM gate fires first
        var prober = new OllamaDirectProber(mapper(), http);

        var rig14090 = coldState("ollama-rig1", "http://rig1:11434", VRAM_4090);
        // Sanity: fp32b must exceed usable 4090 VRAM for the test to be meaningful.
        assertTrue(fp32b > StaticFitPredictor.usableVramMiB(VRAM_4090),
                "pre-condition: inflated 32b footprint must exceed 4090 usable VRAM");

        String picked = prober.pickFittingEndpoint(List.of(rig14090), null, "qwen3:32b", fp32b);

        assertNull(picked, "4090 must be refused for 32b - its usable VRAM is too small");
    }

    @Test
    void pickFittingEndpoint_returnsNullWhenFootprintIsZero() throws Exception {
        var http = mockHttp(emptyPs());
        var prober = new OllamaDirectProber(mapper(), http);
        var rig0 = coldState("ollama-gpu", "http://rig0:11434", VRAM_5090);

        assertNull(prober.pickFittingEndpoint(List.of(rig0), null, "qwen3:32b", 0L),
                "zero footprint means unknown - return null (caller falls back to static)");
    }

    @Test
    void pickFittingEndpoint_refusedWhenResidentLeavesNoRoom() throws Exception {
        // Gate 2: an 8b cold-load fits the 5090 ALONE but not alongside a resident
        // 32b. The degraded path must refuse it (the divergent-duplication gap that
        // let the resident-model spill recur if pickFittingEndpoint only did Gate 1).
        var http = mockHttp(emptyPs()); // 8b not warm anywhere -> cold candidate
        var prober = new OllamaDirectProber(mapper(), http);
        long fp8b = 5979L;
        long resident32b = 27252L;
        var rig05090 = new ProviderState("ollama-gpu", "http://rig0:11434", 1, 0, 0,
                List.of(), true, "2026-06-14T00:00:00Z", VRAM_5090,
                Map.of("qwen3:32b", resident32b), Map.of());
        long usable = StaticFitPredictor.usableVramMiB(VRAM_5090);
        assertTrue(fp8b <= usable, "pre: 8b fits the 5090 alone (Gate 1 passes)");
        assertTrue(fp8b + resident32b > usable,
                "pre: 8b + resident 32b exceeds usable (Gate 2 must refuse)");

        String picked = prober.pickFittingEndpoint(List.of(rig05090), null, "qwen3:8b", fp8b);

        assertNull(picked, "5090 must be refused for an 8b cold-load when a 32b is resident (Gate 2)");
    }

    @Test
    void pickFittingEndpoint_skipsNotReadyProvider() throws Exception {
        // A not-ready provider must be skipped even when the model is warm on it,
        // so pickFittingEndpoint returns null (caller falls back to static endpoint).
        long vramBytes = VRAM_32B_MIB * 1024L * 1024L;
        long fp32b = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        var http = mockHttp(psJson("qwen3:32b", vramBytes)); // warm if probed
        var prober = new OllamaDirectProber(mapper(), http);

        // ready=false: the prober must not consider this endpoint at all.
        var notReady = new ProviderState("ollama-gpu", "http://rig0:11434", 1, 0, 0,
                List.of(), false, "2026-06-14T00:00:00Z", VRAM_5090,
                Map.of(), Map.of());

        assertNull(prober.pickFittingEndpoint(List.of(notReady), null, "qwen3:32b", fp32b),
                "a not-ready provider must be skipped, even when the model is warm on it");
    }

    @Test
    void pickFittingEndpoint_warmStaticEndpointRescuesEmptyState() throws Exception {
        // Degraded window: the KV state is EMPTY, but the agent's own static
        // endpoint has qwen3:32b ALREADY RESIDENT. The model must be credited as a
        // warm fit (zero cold-load, no spill risk) instead of standing aside on an
        // empty KV. This is the JIT degraded-window stand-aside regression.
        long vramBytes = VRAM_32B_MIB * 1024L * 1024L;
        long fp32b = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        var http = mockHttp(psJson("qwen3:32b", vramBytes)); // static endpoint warm
        var prober = new OllamaDirectProber(mapper(), http);

        String picked = prober.pickFittingEndpoint(
                List.of(), "http://static-5090:11434", "qwen3:32b", fp32b);

        assertEquals("http://static-5090:11434", picked,
                "a warm model on the static endpoint must be picked when the KV state is empty");
    }

    @Test
    void pickFittingEndpoint_coldStaticEndpointNotAdmittedOnEmptyState() throws Exception {
        // Degraded window with the model NOT resident on the static endpoint: with no
        // provider VRAM data we cannot gate a cold-load, so refuse rather than risk a
        // CPU spill. Only the WARM static case is rescued, never a cold one.
        long fp32b = (long) (DISK_32B_MIB * ProviderState.ON_DISK_TO_VRAM_FACTOR);
        var http = mockHttp(emptyPs()); // static endpoint cold (not warm)
        var prober = new OllamaDirectProber(mapper(), http);

        assertNull(prober.pickFittingEndpoint(
                List.of(), "http://static-5090:11434", "qwen3:32b", fp32b),
                "a cold static endpoint must not be admitted when the KV state is empty (no VRAM gate)");
    }

    // ---- helper ----

    @SuppressWarnings("rawtypes")
    private static HttpResponse<String> stubResponse(String body) {
        var resp = (HttpResponse<String>) mock(HttpResponse.class);
        when(resp.statusCode()).thenReturn(200);
        when(resp.body()).thenReturn(body);
        return resp;
    }
}
