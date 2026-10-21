package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.nats.NatsLink;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The NATS KV bucket as a {@link TicketStore}: create order, prefix scans, and a bucket that is not there. */
class KvTicketStoreTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static ObjectNode doc(String model) {
        ObjectNode n = MAPPER.createObjectNode();
        n.put("modelName", model);
        return n;
    }

    private static KvTicketStore store(InMemoryKv bucket) {
        return new KvTicketStore(bucket.server(KvTicketStore.BUCKET), MAPPER);
    }

    private static KvTicketStore unavailable() {
        NatsLink down = mock(NatsLink.class);
        when(down.isAvailable()).thenReturn(false);
        return new KvTicketStore(down, MAPPER);
    }

    @Test
    void createGivesIncreasingRevisions_andScanReturnsThemByPrefix() {
        var s = store(new InMemoryKv());
        long first = s.create("rig0__a", doc("m1"));
        long second = s.create("rig0__b", doc("m2"));
        s.create("rig1__c", doc("m3"));

        assertTrue(second > first, "a later create gets a later revision");
        List<TicketStore.Entry> rig0 = s.scan("rig0__");
        assertEquals(2, rig0.size());
        assertEquals("m1", rig0.stream().filter(e -> e.key().equals("rig0__a")).findFirst().orElseThrow()
                .doc().path("modelName").asText());
        assertEquals(1, s.scan("rig1__").size());
        assertTrue(s.scan("rig9__").isEmpty());
    }

    @Test
    void createOfAnExistingKeyFails() {
        var s = store(new InMemoryKv());
        s.create("rig0__a", doc("m1"));
        assertThrows(IllegalStateException.class, () -> s.create("rig0__a", doc("m1")));
    }

    @Test
    void deleteRemovesTheEntry_andIsIdempotent() {
        var s = store(new InMemoryKv());
        s.create("rig0__a", doc("m1"));
        s.delete("rig0__a");
        s.delete("rig0__a");
        assertTrue(s.scan("rig0__").isEmpty());
    }

    @Test
    void putReplacesAnEntry() {
        var s = store(new InMemoryKv());
        s.put("residency__rig0__m", doc("m1"));
        s.put("residency__rig0__m", doc("m2"));
        List<TicketStore.Entry> found = s.scan("residency__rig0__");
        assertEquals(1, found.size());
        assertEquals("m2", found.get(0).doc().path("modelName").asText());
    }

    @Test
    void anUnreadableEntryIsSkippedNotFatal() {
        var bucket = new InMemoryKv();
        var s = store(bucket);
        s.create("rig0__good", doc("m1"));
        bucket.data.put("rig0__bad", "{not json".getBytes());

        List<TicketStore.Entry> found = s.scan("rig0__");
        assertEquals(1, found.size());
        assertEquals("rig0__good", found.get(0).key());
    }

    @Test
    void aBucketThatIsNotThereIsUnavailable_andNeverBlocksACaller() {
        var s = unavailable();
        assertFalse(s.available());
        assertTrue(s.scan("rig0__").isEmpty());
        s.put("k", doc("m"));
        s.delete("k");
        assertThrows(IllegalStateException.class, () -> s.create("k", doc("m")));
    }

    @Test
    void exclusiveRunsTheActionAtOnce() {
        assertEquals("ran", store(new InMemoryKv()).exclusive(() -> "ran"));
    }
}
