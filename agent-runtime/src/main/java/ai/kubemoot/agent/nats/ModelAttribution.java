package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ModelUse;
import ai.kubemoot.agent.util.GpuLabels;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * The signal metadata that names where an agent's inference calls ran: the model
 * and server the scheduler placed each call on, not the model the agent is
 * configured to prefer. When the calls behind one signal ran in more than one
 * place, the signal names the last call's model and server and lists every
 * distinct model@server in {@code modelsUsed}. A signal with no known call keeps
 * the configured model that {@code DiscussionSubscriber.publishSignal} seeds.
 */
final class ModelAttribution {

    static final String FIELD_MODEL_NAME = "modelName";
    static final String FIELD_MODELS_USED = "modelsUsed";

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
        return meta;
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
