package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.support.Validator;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doThrow;

/**
 * The residency overlay records a model left loaded on a server under a key that
 * NATS KV accepts, whatever characters the model name holds, and reads it back.
 */
class ResidencyOverlayTest {

    private static final String RIG1 = "ollama-rig1";
    private static final String MODEL = "qwen3:14b";
    private static final long MODEL_MIB = 12_224;

    private static TicketManager tickets(InMemoryKv bucket) {
        return new TicketManager(bucket.server(TicketManager.TICKETS_BUCKET), new ObjectMapper());
    }

    // ---- the key ----

    @Test
    void residencyKey_encodesTheColonInAModelName() {
        String key = TicketManager.residencyKey(RIG1, MODEL);

        assertEquals("residency__ollama-rig1__qwen3_3a14b", key);
        assertDoesNotThrow(() -> Validator.validateNonWildcardKvKeyRequired(key));
    }

    @Test
    void residencyKey_isLegalAndDistinctForUnusualNames() {
        List<String> models = List.of("qwen3:14b", "qwen3_3a14b", "hf.co/org/model:Q4_K_M",
                "llama3.1:8b-instruct", "model@sha256:abc", "mod\u00e8le:latest", "\u6a21\u578b:7b", "a b");
        Set<String> keys = new HashSet<>();
        for (String model : models) {
            String key = TicketManager.residencyKey("ollama.gpu", model);
            assertDoesNotThrow(() -> Validator.validateNonWildcardKvKeyRequired(key), key);
            assertTrue(key.startsWith(TicketManager.residencyPrefix("ollama.gpu")), key);
            keys.add(key);
        }
        assertEquals(models.size(), keys.size(), "distinct model names never share a key");
    }

    @Test
    void residencyKey_staysOutOfTheServersTicketScan() {
        assertFalse(TicketManager.residencyKey(RIG1, MODEL).startsWith(TicketManager.keyPrefixFor(RIG1)));
    }

    // ---- written, then read back ----

    @Test
    void recordedResidency_isReadBackByTheOverlay() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);

        tickets.recordResidency(RIG1, MODEL, MODEL_MIB);

        assertEquals(Map.of(MODEL, MODEL_MIB), tickets.residentFootprintsFor(RIG1));
        assertTrue(bucket.data.containsKey("residency__ollama-rig1__qwen3_3a14b"));
    }

    @Test
    void recordedResidency_refreshesUnderOneKey() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);

        tickets.recordResidency(RIG1, MODEL, MODEL_MIB);
        tickets.recordResidency(RIG1, MODEL, MODEL_MIB + 100);

        assertEquals(1, bucket.data.size());
        assertEquals(Map.of(MODEL, MODEL_MIB + 100), tickets.residentFootprintsFor(RIG1));
    }

    @Test
    void residency_isScopedToItsServer() {
        var tickets = tickets(new InMemoryKv());

        tickets.recordResidency(RIG1, MODEL, MODEL_MIB);
        tickets.recordResidency("ollama", "qwen3:32b", 27_252);

        assertEquals(Map.of(MODEL, MODEL_MIB), tickets.residentFootprintsFor(RIG1));
        assertEquals(Map.of("qwen3:32b", 27_252L), tickets.residentFootprintsFor("ollama"));
        assertTrue(tickets.residentFootprintsFor("ollama-gpu").isEmpty());
    }

    @Test
    void residency_skipsAnEntryWrittenForAnotherServer() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        String foreign = "{\"server\":\"other\",\"modelName\":\"qwen3:8b\",\"modelFootprintMiB\":5000}";
        bucket.kv.put(TicketManager.residencyPrefix(RIG1) + "x", foreign.getBytes(java.nio.charset.StandardCharsets.UTF_8));

        assertTrue(tickets.residentFootprintsFor(RIG1).isEmpty());
    }

    @Test
    void residency_isNotCountedAsAnInFlightTicket() {
        var tickets = tickets(new InMemoryKv());

        tickets.recordResidency(RIG1, MODEL, MODEL_MIB);

        assertEquals(0, tickets.activeCountFor(RIG1));
        assertEquals(0L, tickets.activeFootprintFor(RIG1));
    }

    @Test
    void clearResidency_removesTheEntry() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.recordResidency(RIG1, MODEL, MODEL_MIB);

        tickets.clearResidency(RIG1, MODEL);

        assertTrue(bucket.data.isEmpty());
        assertTrue(tickets.residentFootprintsFor(RIG1).isEmpty());
    }

    @Test
    void recordResidency_ignoresBlankInputsAndNonPositiveFootprints() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);

        tickets.recordResidency(null, MODEL, MODEL_MIB);
        tickets.recordResidency("", MODEL, MODEL_MIB);
        tickets.recordResidency(RIG1, null, MODEL_MIB);
        tickets.recordResidency(RIG1, "", MODEL_MIB);
        tickets.recordResidency(RIG1, MODEL, 0);
        tickets.recordResidency(RIG1, MODEL, -1);

        assertTrue(bucket.data.isEmpty());
        assertTrue(tickets.residentFootprintsFor(null).isEmpty());
    }

    // ---- a failed write ----

    @Test
    void failedResidencyWrite_doesNotReachTheCallerOrBreakClaiming() throws Exception {
        var bucket = new InMemoryKv();
        doThrow(new IOException("bucket unavailable")).when(bucket.kv).put(anyString(), any(byte[].class));
        doThrow(new IOException("bucket unavailable")).when(bucket.kv).delete(anyString());
        var tickets = tickets(bucket);

        assertDoesNotThrow(() -> tickets.recordResidency(RIG1, MODEL, MODEL_MIB));
        assertDoesNotThrow(() -> tickets.clearResidency(RIG1, MODEL));

        assertTrue(tickets.residentFootprintsFor(RIG1).isEmpty());
        assertTrue(tickets.claim(RIG1, MODEL_MIB, 0, 20_000, MODEL).isPresent(), "claiming still works");
        assertEquals(1, tickets.activeCountFor(RIG1));
    }

    @Test
    void anIllegalKeyFailsInTheBucketAsInJnats() {
        var bucket = new InMemoryKv();

        assertThrows(IllegalArgumentException.class,
                () -> bucket.kv.put("residency__ollama-rig1__qwen3:14b", new byte[0]));
    }
}
