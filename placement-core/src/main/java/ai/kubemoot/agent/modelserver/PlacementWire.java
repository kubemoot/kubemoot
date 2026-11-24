package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

/**
 * The placement service's wire format and its JSON codec, shared by the service and every client.
 *
 * <p>Records are read and written field by field through {@link JsonNode}, never with
 * {@code readValue} on a record: record reflection fails silently in a native image.</p>
 */
public final class PlacementWire {

    /** Reply code: no server has room now; waiting for capacity can still place the call. */
    public static final String NO_FIT = "kubemoot_no_fit";

    /** One acceptable model with the memory a call on it needs. */
    public record ModelNeed(String name, long footprintMiB, long kvMiB) {}

    /**
     * What one call needs placed. {@code models} lists the acceptable models, preferred first.
     * {@code policy} is the placement policy the caller's configuration selects (algorithm, learned
     * or shadow); empty means the service default. {@code phase} names the call (mulling, triage).
     */
    public record PlaceRequest(AgentIdentity identity, String threadId, String phase, String policy,
                               List<ModelNeed> models, long promptTokens, boolean mayUnload) {
        public PlaceRequest {
            threadId = threadId == null ? "" : threadId;
            phase = phase == null ? "" : phase;
            policy = policy == null ? "" : policy;
            models = models == null ? List.of() : List.copyOf(models);
        }

        /** The request as the placement code takes it, against the servers in {@code states}. */
        public CallPlanner.PlacementRequest toPlacementRequest(List<ServerState> states) {
            Map<String, ModelNeed> byName = new java.util.HashMap<>();
            models.forEach(m -> byName.putIfAbsent(m.name(), m));
            String preferred = models.get(0).name();
            List<String> alternatives = models.subList(1, models.size()).stream().map(ModelNeed::name).toList();
            return new CallPlanner.PlacementRequest(preferred, alternatives, states,
                    m -> byName.containsKey(m) ? byName.get(m).footprintMiB() : 0L,
                    m -> byName.containsKey(m) ? byName.get(m).kvMiB() : 0L,
                    promptTokens, mayUnload);
        }

        /** The request a caller builds from the placement code's own request. */
        public static PlaceRequest of(AgentIdentity identity, String threadId, String phase, String policy,
                                      CallPlanner.PlacementRequest r) {
            List<ModelNeed> needs = r.acceptable().stream()
                    .map(m -> new ModelNeed(m, r.footprint().applyAsLong(m), r.kv().applyAsLong(m))).toList();
            return new PlaceRequest(identity, threadId, phase, policy, needs, r.promptTokens(), r.mayUnload());
        }
    }

    /** Where a call runs: the server, the model, the claimed ticket and why. */
    public record PlaceReply(String server, String endpoint, String model, long footprintMiB, boolean queued,
                             boolean coldLoad, List<String> evicted, Ticket ticket, PlacementDecision decision,
                             int active) {

        /**
         * The reply for a placement the service made. {@code active} is the calls in flight on the
         * server once this one holds its ticket, so a caller can record the load without asking again.
         */
        public static PlaceReply of(CallPlanner.Placement p, int active) {
            ServerState s = p.pick().server();
            return new PlaceReply(s.name(), s.endpointFor(p.model()), p.model(), p.footprintMiB(), p.queued(),
                    p.coldLoad(), p.evicted(), p.pick().ticket(), p.decision(), active);
        }

        /** The placement this reply describes, for {@code server} as the caller reads it. */
        public CallPlanner.Placement toPlacement(ServerState serverState) {
            String why = decision == null ? "placed by the placement service" : decision.reason();
            Pick pick = new Pick(serverState, ticket, FitScore.yes(0L, why), queued);
            return new CallPlanner.Placement(pick, model, footprintMiB, evicted, decision);
        }
    }

    /** A refusal: {@code code} is {@link #NO_FIT} or a {@link PlacementFeasibility} code. */
    public record Refusal(String code, String reason) {}

    private PlacementWire() {}

    // ---- identity ----

    public static ObjectNode identityJson(ObjectMapper m, AgentIdentity id) {
        ObjectNode n = m.createObjectNode();
        n.put("namespace", id.namespace());
        n.put("crew", id.crew());
        n.put("agent", id.agentName());
        return n;
    }

    public static AgentIdentity identityFrom(JsonNode n) {
        return new AgentIdentity(n.path("namespace").asText(""), n.path("agent").asText(""),
                n.path("crew").asText(""));
    }

    // ---- request ----

    public static ObjectNode requestJson(ObjectMapper m, PlaceRequest r) {
        ObjectNode n = identityJson(m, r.identity());
        n.put("threadId", r.threadId());
        n.put("phase", r.phase());
        n.put(WireFields.POLICY, r.policy());
        n.put("promptTokens", r.promptTokens());
        n.put("mayUnload", r.mayUnload());
        ArrayNode models = n.putArray("models");
        for (ModelNeed need : r.models()) {
            ObjectNode o = models.addObject();
            o.put("name", need.name());
            o.put(WireFields.FOOTPRINT_MIB, need.footprintMiB());
            o.put("kvMiB", need.kvMiB());
        }
        return n;
    }

    /**
     * The request in {@code n}.
     *
     * @throws IllegalArgumentException when no model is named
     */
    public static PlaceRequest requestFrom(JsonNode n) {
        List<ModelNeed> needs = new ArrayList<>();
        for (JsonNode o : n.path("models")) {
            String name = o.path("name").asText("");
            if (!name.isBlank()) {
                needs.add(new ModelNeed(name, o.path(WireFields.FOOTPRINT_MIB).asLong(0L), o.path("kvMiB").asLong(0L)));
            }
        }
        if (needs.isEmpty()) {
            throw new IllegalArgumentException("models is empty");
        }
        return new PlaceRequest(identityFrom(n), n.path("threadId").asText(""), n.path("phase").asText(""),
                n.path(WireFields.POLICY).asText(""), needs, n.path("promptTokens").asLong(0L),
                n.path("mayUnload").asBoolean(true));
    }

    // ---- reply ----

    public static ObjectNode replyJson(ObjectMapper m, PlaceReply r) {
        ObjectNode n = m.createObjectNode();
        n.put(WireFields.SERVER, r.server());
        n.put("endpoint", r.endpoint());
        n.put(WireFields.MODEL, r.model());
        n.put(WireFields.FOOTPRINT_MIB, r.footprintMiB());
        n.put("queued", r.queued());
        n.put("coldLoad", r.coldLoad());
        n.put("active", r.active());
        ArrayNode ev = n.putArray("evicted");
        r.evicted().forEach(ev::add);
        n.set("ticket", ticketJson(m, r.ticket()));
        if (r.decision() != null) {
            n.set("decision", decisionJson(m, r.decision()));
        }
        return n;
    }

    public static PlaceReply replyFrom(JsonNode n) {
        List<String> evicted = new ArrayList<>();
        n.path("evicted").forEach(e -> evicted.add(e.asText()));
        JsonNode decision = n.path("decision");
        return new PlaceReply(n.path(WireFields.SERVER).asText(""), n.path("endpoint").asText(""), n.path(WireFields.MODEL).asText(""),
                n.path(WireFields.FOOTPRINT_MIB).asLong(0L), n.path("queued").asBoolean(false),
                n.path("coldLoad").asBoolean(false), evicted, ticketFrom(n.path("ticket")),
                decision.isObject() ? decisionFrom(decision) : null, n.path("active").asInt(0));
    }

    // ---- ticket ----

    static ObjectNode ticketJson(ObjectMapper m, Ticket t) {
        ObjectNode n = m.createObjectNode();
        if (t == null) {
            return n;
        }
        n.put(WireFields.TICKET_ID, t.ticketId());
        n.put(WireFields.SERVER, t.server());
        n.put("modelFootprintMiB", t.modelFootprintMiB());
        n.put("holderId", t.holderId());
        n.put("claimedAt", t.claimedAt());
        n.put("kvCacheFootprintMiB", t.kvCacheFootprintMiB());
        if (t.modelName() != null) {
            n.put(WireFields.MODEL_NAME, t.modelName());
        }
        return n;
    }

    static Ticket ticketFrom(JsonNode n) {
        if (!n.has(WireFields.TICKET_ID)) {
            return null;
        }
        return new Ticket(n.path(WireFields.TICKET_ID).asText(), n.path(WireFields.SERVER).asText(""),
                n.path("modelFootprintMiB").asLong(0L), n.path("holderId").asText(""),
                n.path("claimedAt").asText(""), n.path("kvCacheFootprintMiB").asLong(0L),
                n.has(WireFields.MODEL_NAME) ? n.path(WireFields.MODEL_NAME).asText() : null);
    }

    // ---- decision ----

    static ObjectNode decisionJson(ObjectMapper m, PlacementDecision d) {
        ObjectNode n = m.createObjectNode();
        n.put(WireFields.POLICY, d.policy());
        n.put(WireFields.REASON, d.reason());
        if (d.predicted() != null) {
            n.set(WireFields.PREDICTED, predictionJson(m, d.predicted()));
        }
        ArrayNode alts = n.putArray("alternatives");
        for (PlacementDecision.Alternative a : d.alternatives()) {
            ObjectNode o = alts.addObject();
            o.put(WireFields.SERVER, a.server());
            o.put(WireFields.MODEL, a.model());
            o.put("warm", a.warm());
            o.put("startsNow", a.startsNow());
            if (a.predicted() != null) {
                o.set(WireFields.PREDICTED, predictionJson(m, a.predicted()));
            }
        }
        return n;
    }

    static PlacementDecision decisionFrom(JsonNode n) {
        List<PlacementDecision.Alternative> alts = new ArrayList<>();
        for (JsonNode a : n.path("alternatives")) {
            alts.add(new PlacementDecision.Alternative(a.path(WireFields.SERVER).asText(""), a.path(WireFields.MODEL).asText(""),
                    a.path("warm").asBoolean(false), a.path("startsNow").asBoolean(false),
                    predictionFrom(a.path(WireFields.PREDICTED))));
        }
        return new PlacementDecision(n.path(WireFields.POLICY).asText(PlacementDecision.ALGORITHM),
                n.path(WireFields.REASON).asText(""), alts, predictionFrom(n.path(WireFields.PREDICTED)));
    }

    static ObjectNode predictionJson(ObjectMapper m, Prediction p) {
        ObjectNode n = m.createObjectNode();
        n.put("queue", p.queue());
        n.put("load", p.load());
        n.put("prompt", p.prompt());
        n.put("generate", p.generate());
        return n;
    }

    static Prediction predictionFrom(JsonNode n) {
        if (!n.isObject()) {
            return null;
        }
        return new Prediction(n.path("queue").asDouble(0.0), n.path("load").asDouble(0.0),
                n.path("prompt").asDouble(0.0), n.path("generate").asDouble(0.0));
    }

    // ---- refusal ----

    public static ObjectNode refusalJson(ObjectMapper m, Refusal r) {
        ObjectNode n = m.createObjectNode();
        n.put("code", r.code());
        n.put(WireFields.REASON, r.reason());
        return n;
    }

    public static Refusal refusalFrom(JsonNode n) {
        return new Refusal(n.path("code").asText(NO_FIT), n.path(WireFields.REASON).asText(""));
    }
}
