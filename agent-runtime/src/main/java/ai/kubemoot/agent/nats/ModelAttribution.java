package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ModelUse;
import ai.kubemoot.agent.util.GpuLabels;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * The signal metadata that names where an agent's inference calls ran: the model
 * and server the scheduler placed each call on, not the model the agent is
 * configured to prefer. When the calls behind one signal ran in more than one
 * place, the signal names the last call's model and server and lists every
 * distinct model@server in {@code modelsUsed}. A signal with no known call keeps
 * the configured model that {@code DiscussionSubscriber.publishSignal} seeds.
 *
 * <p>Each call the scheduler placed also reports its placement as
 * {@code placementCalls}: the policy, the reason, the options weighed, what was
 * predicted and what the call measured. The coordinator copies them to the
 * discussion receipt.</p>
 */
final class ModelAttribution {

    static final String FIELD_MODEL_NAME = "modelName";
    static final String FIELD_MODELS_USED = "modelsUsed";
    static final String FIELD_PLACEMENT_CALLS = "placementCalls";

    private ModelAttribution() {}

    /**
     * {@code modelName} (the last call's model) and {@code modelsUsed} (each distinct
     * model@server, in call order); empty when no call with a known model ran.
     */
    static Map<String, Object> of(List<ModelUse> uses) {
        List<ModelUse> known = known(uses);
        if (known.isEmpty()) {
            return Map.of();
        }
        var meta = new HashMap<String, Object>();
        meta.put(FIELD_MODEL_NAME, last(known).model());
        meta.put(FIELD_MODELS_USED, known.stream().map(ModelUse::label).distinct().toList());
        putPlacementCalls(meta, known);
        return meta;
    }

    /**
     * {@code meta} with the placement of {@code earlier}, a call made before the ones it
     * describes (an agent's triage before its evaluation), put first in
     * {@code placementCalls}. Returns {@code meta} itself when that call was not placed
     * by the scheduler.
     */
    static Map<String, Object> withEarlierCall(Map<String, Object> meta, ModelUse earlier) {
        if (earlier == null || earlier.call() == null) {
            return meta;
        }
        var out = new HashMap<String, Object>(meta);
        List<Object> calls = new ArrayList<>();
        calls.add(earlier.call().snapshot(true));
        if (meta.get(FIELD_PLACEMENT_CALLS) instanceof List<?> later) {
            calls.addAll(later);
        }
        out.put(FIELD_PLACEMENT_CALLS, calls);
        return out;
    }

    /** Adds the snapshot of each placed call, in call order, when there is any. */
    private static void putPlacementCalls(Map<String, Object> meta, List<ModelUse> uses) {
        List<Map<String, Object>> calls = uses.stream().map(ModelUse::call).filter(Objects::nonNull)
                .map(c -> c.snapshot(true)).toList();
        if (!calls.isEmpty()) {
            meta.put(FIELD_PLACEMENT_CALLS, calls);
        }
    }

    /**
     * The server the last call ran on, or {@code fallback} when no call is known or
     * the last one went to the static endpoint rather than a picked server.
     */
    static String gpuLabel(List<ModelUse> uses, String fallback) {
        List<ModelUse> known = known(uses);
        String server = known.isEmpty() ? null : GpuLabels.fromServer(last(known).server());
        return server != null ? server : fallback;
    }

    private static List<ModelUse> known(List<ModelUse> uses) {
        return uses == null ? List.of() : uses.stream().filter(u -> !u.model().isEmpty()).toList();
    }

    private static ModelUse last(List<ModelUse> uses) {
        return uses.get(uses.size() - 1);
    }
}
