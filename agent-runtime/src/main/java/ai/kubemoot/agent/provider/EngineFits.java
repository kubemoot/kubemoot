package ai.kubemoot.agent.provider;

/** The {@link EngineFit} for a provider's engine type. */
public final class EngineFits {

    private EngineFits() {}

    /**
     * Ollama's fit when the type is absent or {@code ollama} (providers published before
     * the type was), else the served-model fit, which holds for any OpenAI-compatible
     * server that reserves its memory at start. Agrees with {@link ChatEngines#isOllama}
     * and {@link ProviderSelector#driverFor} on which engines are Ollama.
     */
    public static EngineFit forType(String type) {
        return ChatEngines.isOllama(type) ? OllamaFit.INSTANCE : ServedModelFit.INSTANCE;
    }

    /**
     * The footprint a placement request carries for {@code model}, given the footprint
     * {@code reportedMiB} that providers report (zero when none does). A model that only
     * providers whose engine reserved its memory at start host has none to report and
     * needs none; it carries the ticket marker, because a zero footprint leaves every
     * provider out of placement.
     */
    public static long placementFootprintMiB(java.util.List<ProviderState> states, String model, long reportedMiB) {
        if (reportedMiB > 0 || states == null || states.stream().noneMatch(p -> p.hostsWithoutMemoryBudget(model))) {
            return reportedMiB;
        }
        return ServedModelFit.TICKET_MARKER_MIB;
    }
}
