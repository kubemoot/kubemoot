package ai.kubemoot.agent.util;

/**
 * Derives a stable SERVER label for telemetry from a model-server endpoint
 * URL or ModelServer CR name. The label is the server IDENTIFIER (the
 * endpoint's namespace/host, or the CR name) - it is deliberately NOT a hardcoded
 * GPU model. Which physical GPU a server runs is cluster topology that the
 * operator discovers (ModelServer.status.capacity.gpuModel via DCGM); a caller
 * that holds the ServerState should prefer that discovered model for display.
 * This utility only provides a name-derived fallback so a label is never empty,
 * and bakes no assumption about the cluster's GPUs or models.
 */
public final class GpuLabels {

    private GpuLabels() {}

    /**
     * Server label derived from an endpoint URL. Extracts the host's namespace
     * component, e.g. "http://ollama.ollama-rig1:11434" -> "ollama-rig1". Returns
     * "server" when nothing usable can be extracted. No GPU/model assumptions.
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
     * GPU badge reflects WHERE the inference actually ran (the per-call JIT pick).
     * Returns {@code null} for empty/unknown names so callers fall back to the
     * endpoint-derived label.
     */
    public static String fromServer(String serverName) {
        if (serverName == null || serverName.isEmpty()) return null;
        return serverName;
    }
}
