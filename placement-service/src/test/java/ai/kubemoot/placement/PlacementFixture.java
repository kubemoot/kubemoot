package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.DemandBoard;
import ai.kubemoot.agent.modelserver.InMemoryKv;
import ai.kubemoot.agent.modelserver.LearningFitPredictor;
import ai.kubemoot.agent.modelserver.OllamaDriver;
import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.TicketManager;
import ai.kubemoot.agent.nats.NatsLink;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Map;

/**
 * A placement service over an in-memory server-state bucket: the real selector, ticket manager and
 * engine, with the tickets in a {@link TicketTable} and no NATS.
 */
final class PlacementFixture {

    static final ObjectMapper M = new ObjectMapper();
    static final String SERVER = "rig0";
    static final String MODEL = "qwen3:14b";

    final InMemoryKv state = new InMemoryKv();
    final InMemoryKv demand = new InMemoryKv();
    final TicketTable table = new TicketTable(600_000, System::currentTimeMillis);
    final TicketManager tickets = new TicketManager(table, M);
    final ServerSelector selector;
    final CapacityVersion capacity = new CapacityVersion(table);
    final PlacementEngine engine;
    final PlacementResource resource;

    PlacementFixture() {
        NatsLink link = InMemoryKv.link(Map.of(ServerSelector.STATE_BUCKET, state, DemandBoard.BUCKET, demand));
        selector = new ServerSelector(link, M, tickets, new LearningFitPredictor(), new OllamaDriver(null));
        engine = new PlacementEngine(selector, tickets, table, link, M,
                ServiceSettings.forTest("placement-test", Duration.ofSeconds(15), Duration.ofMinutes(10)),
                new SimpleMeterRegistry());
        resource = new PlacementResource(engine, capacity, table, M, new SimpleMeterRegistry());
        table.activate();
    }

    /** Publishes a ready server with {@code loaded} resident and {@code available} downloaded. */
    void publishServer(String name, long vramMiB, int maxParallel, Map<String, Long> loaded,
                       Map<String, Long> available, long context) {
        ObjectNode n = M.createObjectNode();
        n.put("name", name);
        n.put("endpoint", "http://" + name + ":11434");
        n.put("maxParallel", maxParallel);
        n.put("ready", true);
        n.put("lastProbedAt", java.time.Instant.now().toString());
        n.put("totalVramMiB", vramMiB);
        n.put("contextLength", context);
        var loadedNames = n.putArray("loadedModels");
        loaded.keySet().forEach(loadedNames::add);
        var lf = n.putObject("loadedModelFootprintsMiB");
        loaded.forEach(lf::put);
        var af = n.putObject("availableModelFootprintsMiB");
        available.forEach(af::put);
        try {
            state.kv.put(name, M.writeValueAsString(n).getBytes(StandardCharsets.UTF_8));
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
        selector.invalidateCache();
    }

    /** The common case: one 32 GiB server with the model warm. */
    void publishWarmServer() {
        publishServer(SERVER, 32_768, 2, Map.of(MODEL, 10_000L), Map.of(MODEL, 10_000L), 8_192);
    }

    /** A placement request body for the agent {@code agent}. */
    static ObjectNode body(String agent, String threadId, String model, long footprintMiB, long promptTokens,
                           boolean mayUnload) {
        ObjectNode n = M.createObjectNode();
        n.put("namespace", "crew-ns");
        n.put("crew", "pilot");
        n.put("agent", agent);
        n.put("threadId", threadId);
        n.put("phase", "mulling");
        n.put("promptTokens", promptTokens);
        n.put("mayUnload", mayUnload);
        var m = n.putArray("models").addObject();
        m.put("name", model);
        m.put("footprintMiB", footprintMiB);
        m.put("kvMiB", 256);
        return n;
    }

    static ObjectNode body(String agent) {
        return body(agent, "t-" + agent, MODEL, 10_000, 500, true);
    }

    /** Identity-only body, for the calls that name just the agent. */
    static ObjectNode identity(String agent) {
        ObjectNode n = M.createObjectNode();
        n.put("namespace", "crew-ns");
        n.put("crew", "pilot");
        n.put("agent", agent);
        return n;
    }
}
