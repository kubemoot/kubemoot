package ai.kubemoot.agent.util;

/**
 * Derives a stable SERVER label for telemetry from a model-server endpoint
 * URL or ModelServer CR name. The label is the server IDENTIFIER (the
 * endpoint's namespace/host, or the CR name) - it is deliberately NOT a hardcoded
 * accelerator model. Which physical accelerator a server runs is cluster topology that the
 * operator discovers (ModelServer.status.capacity.acceleratorModel); a caller
 * that holds the ServerState should prefer that discovered model for display.
 * This utility only provides a name-derived fallback so a label is never empty,
 * and bakes no assumption about the cluster's accelerators or models.
 */
public final class AcceleratorLabels {

    /** Bus metadata key of the accelerator label. */
    public static final String FIELD = "acceleratorLabel";

    /** The old bus metadata key of the label: sent with {@link #FIELD} for one release, then removed. */
    public static final String DEPRECATED_FIELD = "gpuLabel";

    private AcceleratorLabels() {}

    /** Puts {@code label} in {@code metadata} under its current key and the deprecated old one. */
    public static void put(java.util.Map<String, Object> metadata, String label) {
        metadata.put(FIELD, label);
        metadata.put(DEPRECATED_FIELD, label);
    }

    /**
     * The label a signal's metadata carries: the current key, else the old one from an older
     * publisher. Null when the metadata has neither.
     */
    public static String read(com.fasterxml.jackson.databind.JsonNode metadata) {
        if (metadata == null) return null;
        if (metadata.has(FIELD)) return metadata.get(FIELD).asText();
        return metadata.has(DEPRECATED_FIELD) ? metadata.get(DEPRECATED_FIELD).asText() : null;
    }

    /**
     * Server label derived from an endpoint URL. Extracts the host's namespace
     * component, e.g. "http://ollama.ollama-a:11434" -> "ollama-a". Returns
     * "server" when nothing usable can be extracted. No accelerator or model assumptions.
     */
    public static String fromEndpoint(String endpoint) {
        if (endpoint == null || endpoint.isBlank()) return "server";
        String s = endpoint;
        int scheme = s.indexOf("://");
        if (scheme >= 0) s = s.substring(scheme + 3);
        int slash = s.indexOf('/');
        if (slash >= 0) s = s.substring(0, slash);
        int colon = s.indexOf(':');
        if (colon >= 0) s = s.substring(0, colon);
        if (s.isBlank()) return "server";
        // Kubernetes service host is "service.namespace[.svc.cluster.local]"; the
        // namespace (component 1) is the stable per-server identifier. Fall back
        // to the whole host when there is no namespace component.
        String[] parts = s.split("\\.");
        return parts.length >= 2 ? parts[1] : parts[0];
    }

    /**
     * The ModelServer CR name IS the server identifier - returned as-is so the
     * accelerator badge reflects WHERE the inference actually ran (the per-call JIT pick).
     * Returns {@code null} for empty/unknown names so callers fall back to the
     * endpoint-derived label.
     */
    public static String fromServer(String serverName) {
        if (serverName == null || serverName.isEmpty()) return null;
        return serverName;
    }
}
