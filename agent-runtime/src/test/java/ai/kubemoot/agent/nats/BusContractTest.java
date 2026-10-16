package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.ModelWarmupService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.provider.NoFitException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.networknt.schema.Schema;
import com.networknt.schema.SchemaLocation;
import com.networknt.schema.SchemaRegistry;
import com.networknt.schema.SpecificationVersion;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.ObjectStoreManagement;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * Validates what agent-runtime writes on the bus against the published bus contract,
 * the JSON Schema files in operator/pkg/bus/schema: every message must match its
 * schema, and every field it carries must be one the schema declares.
 */
class BusContractTest {

    private static final Path SCHEMA_DIR = Path.of("..", "operator", "pkg", "bus", "schema");
    private static final String BASE = "https://kubemoot.test/bus/";
    private static final String MESSAGE = "discussion-message.json";
    private static final String METADATA = "discussion-metadata.json";
    private static final String SUBJECT = "kubemoot.discuss.ns-a.nocrew.general.t1";
    private static final String MODEL = "qwen3:14b";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static SchemaRegistry registry;

    @BeforeAll
    static void loadSchemas() throws IOException {
        Map<String, String> schemas = new HashMap<>();
        try (Stream<Path> files = Files.list(SCHEMA_DIR)) {
            for (Path f : files.filter(p -> p.toString().endsWith(".json")).toList()) {
                schemas.put(BASE + f.getFileName(), Files.readString(f));
            }
        }
        registry = SchemaRegistry.withDefaultDialect(SpecificationVersion.DRAFT_7, b -> b.schemas(schemas));
    }

    @Test
    void capacitySignalsMatchTheContract() throws Exception {
        var conn = mock(Connection.class);
        var wait = subscriber(conn, mock(ChatService.class)).new ThreadCapacityWait(SUBJECT, "t1", "5090");

        wait.onWaiting(MODEL);
        wait.onCapacity(MODEL);

        var payloads = ArgumentCaptor.forClass(byte[].class);
        verify(conn, times(2)).publish(anyString(), payloads.capture());
        for (byte[] payload : payloads.getAllValues()) {
            assertConforms(MESSAGE, MAPPER.readTree(payload));
        }
    }

    @Test
    void aCapacityStandAsideMatchesTheContract() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChatWithTokens(anyString(), anyString())).thenThrow(NoFitException.promptTooLarge("m:8b", "too long"));

        subscriber(conn, chat).runTriage(SUBJECT, "t1", "conversation", "4090", System.currentTimeMillis());

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        JsonNode msg = MAPPER.readTree(payload.getValue());
        assertEquals("stand_aside", msg.path("messageType").asText());
        assertConforms(MESSAGE, msg);
    }

    @Test
    void aFindingNamingTheModelsItsCallsRanOnMatchesTheContract() throws Exception {
        var conn = mock(Connection.class);
        var result = new ChatService.ChatResult("c", "finding", MODEL, "t1", 10, 5, "provider-b", "warm",
                List.of(), List.of(new ai.kubemoot.agent.chat.ModelUse("other:32b", "provider-a"),
                        new ai.kubemoot.agent.chat.ModelUse(MODEL, "provider-b")));

        subscriber(conn, mock(ChatService.class)).classifyAndPublishResult(SUBJECT, "t1", result,
                new DiscussionSubscriber.SignalTiming(100, 1, 10, 5), "4090");

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        JsonNode msg = MAPPER.readTree(payload.getValue());
        assertEquals(2, msg.path("metadata").path("modelsUsed").size());
        assertConforms(MESSAGE, msg);
    }

    @Test
    void aSpilledArtifactMatchesTheContract() throws Exception {
        var conn = mock(Connection.class);
        when(conn.objectStoreManagement()).thenReturn(mock(ObjectStoreManagement.class));
        when(conn.objectStore(anyString())).thenReturn(mock(ObjectStore.class));
        var metadata = new HashMap<String, Object>();

        subscriber(conn, mock(ChatService.class)).resolveContentField(conn, "t1", "agree", "z".repeat(5000), metadata);

        var notice = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(eq("kubemoot.artifacts.ns-a.nocrew.t1"), notice.capture());
        assertConforms("artifact-notice.json", MAPPER.readTree(notice.getValue()));
        assertConforms(METADATA, MAPPER.valueToTree(metadata));
    }

    @Test
    void aHeartbeatMatchesTheContract() throws Exception {
        var service = new AgentHeartbeatService(mock(NatsConnectionProvider.class), mock(ModelWarmupService.class),
                mock(AgentProperties.class));
        Instant now = Instant.parse("2026-10-15T12:00:00.123456789Z");

        for (Instant lastInference : new Instant[] {null, now}) {
            String json = service.buildHeartbeatJson("pilot-k8s", now, true, false, MODEL, lastInference, 60);
            assertConforms("agent-heartbeat.json", MAPPER.readTree(json));
        }
    }

    @Test
    void aSchedulerPerformanceEntryMatchesTheContract() throws Exception {
        var bucket = new ai.kubemoot.agent.provider.InMemoryKv();
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn("pilot-k8s");
        when(props.namespace()).thenReturn(java.util.Optional.of("crew-homelab-pilot"));
        var history = ai.kubemoot.agent.provider.PerformanceHistoryFixtures.direct(
                bucket.provider(ai.kubemoot.agent.provider.PerformanceHistory.BUCKET), MAPPER, props);
        long unknown = ai.kubemoot.agent.provider.CallObservation.UNKNOWN;
        history.record(new ai.kubemoot.agent.provider.CallObservation("ollama-gpu", MODEL,
                ai.kubemoot.agent.provider.CallObservation.KIND_TOOL_TURN, true, true, 4_000, 200, 9_000,
                unknown, unknown, unknown, 2, 1_791_000_000_000L));
        history.recordWait("ollama-gpu", MODEL, 12_000);

        assertEquals(1, bucket.data.size());
        assertConforms("scheduler-performance.json", MAPPER.readTree(bucket.data.values().iterator().next()));
    }

    @Test
    void theContractRejectsWhatAWriterMustNotSend() throws Exception {
        JsonNode valid = MAPPER.readTree("""
                {"messageId": "m", "threadId": "t", "agentName": "a", "messageType": "agree",
                 "metadata": {"signal": "agree"}}""");
        assertConforms(MESSAGE, valid);

        JsonNode withBogus = MAPPER.readTree("""
                {"messageId": "m", "threadId": "t", "agentName": "a", "messageType": "agree",
                 "metadata": {"signal": "agree", "bogus": 1}}""");
        assertFalse(undeclared(MESSAGE, withBogus).isEmpty(), "an undeclared metadata field passed");

        JsonNode unknownType = MAPPER.readTree("""
                {"messageId": "m", "threadId": "t", "agentName": "a", "messageType": "shrug"}""");
        assertFalse(schema(MESSAGE).validate(unknownType).isEmpty(), "an unknown messageType passed");
    }

    private static Schema schema(String file) {
        return registry.getSchema(SchemaLocation.of(BASE + file));
    }

    /** Asserts that node matches the schema in file and carries only declared fields. */
    private static void assertConforms(String file, JsonNode node) throws IOException {
        var errors = schema(file).validate(node);
        assertTrue(errors.isEmpty(), file + ": " + errors + " in " + node);
        Set<String> extra = undeclared(file, node);
        assertTrue(extra.isEmpty(), file + ": undeclared fields " + extra + " in " + node);
    }

    /**
     * The fields of node that its schema does not declare: top-level fields against
     * the schema's properties, and for a discussion message its metadata fields
     * against the metadata schema.
     */
    private static Set<String> undeclared(String file, JsonNode node) throws IOException {
        Set<String> extra = new HashSet<>(fieldsOutside(node, declared(file)));
        if (MESSAGE.equals(file) && node.has("metadata")) {
            for (String f : fieldsOutside(node.get("metadata"), declared(METADATA))) {
                extra.add("metadata." + f);
            }
        }
        return extra;
    }

    private static Set<String> declared(String file) throws IOException {
        Set<String> names = new HashSet<>();
        MAPPER.readTree(SCHEMA_DIR.resolve(file).toFile()).path("properties").fieldNames().forEachRemaining(names::add);
        return names;
    }

    private static Set<String> fieldsOutside(JsonNode node, Set<String> declared) {
        Set<String> extra = new HashSet<>();
        for (Iterator<String> it = node.fieldNames(); it.hasNext(); ) {
            String f = it.next();
            if (!declared.contains(f)) {
                extra.add(f);
            }
        }
        return extra;
    }

    private static DiscussionSubscriber subscriber(Connection conn, ChatService chat) {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        when(natsProvider.getConnection()).thenReturn(conn);
        var metrics = new DiscussionMetrics(new SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chat, metrics,
                DiscussionSubscriberHelpersTest.stubProperties("test-agent", "kubernetes", null, false),
                "http://localhost:11434");
    }
}
