package ai.kubemoot.agent.tracing;

import org.eclipse.microprofile.config.spi.ConfigSource;

import java.util.Map;
import java.util.Set;
import java.util.function.Function;

/**
 * Turns the one setting an installer makes, {@value #ENV} (an OTLP/HTTP collector such as
 * {@code http://otel-collector:4318}, the same value the sidecar takes), into the OpenTelemetry
 * extension's settings. With no endpoint the SDK is disabled: nothing is exported and the
 * runtime passes an incoming trace context on unchanged. With one, spans go to
 * {@code <endpoint>/v1/traces} over OTLP/HTTP, as the sidecar's do (the extension adds the path).
 *
 * <p>The ordinal is low, so an explicit {@code QUARKUS_OTEL_*} setting still wins.</p>
 */
public final class OtlpConfigSource implements ConfigSource {

    /** The environment variable that names the collector. */
    public static final String ENV = "KUBEMOOT_OTLP_ENDPOINT";

    static final String SDK_DISABLED = "quarkus.otel.sdk.disabled";
    static final String TRACES_ENDPOINT = "quarkus.otel.exporter.otlp.traces.endpoint";
    static final String TRACES_PROTOCOL = "quarkus.otel.exporter.otlp.traces.protocol";
    private static final int ORDINAL = 100;

    private final Function<String, String> environment;

    public OtlpConfigSource() {
        this(System::getenv);
    }

    OtlpConfigSource(Function<String, String> env) {
        this.environment = env;
    }

    @Override
    public Set<String> getPropertyNames() {
        return Set.of(SDK_DISABLED, TRACES_ENDPOINT, TRACES_PROTOCOL);
    }

    @Override
    public Map<String, String> getProperties() {
        var all = new java.util.HashMap<String, String>();
        for (String name : getPropertyNames()) {
            String value = getValue(name);
            if (value != null) {
                all.put(name, value);
            }
        }
        return all;
    }

    @Override
    public String getValue(String name) {
        String endpoint = endpoint();
        return switch (name) {
            case SDK_DISABLED -> Boolean.toString(endpoint.isEmpty());
            case TRACES_ENDPOINT -> endpoint.isEmpty() ? null : baseUrl(endpoint);
            case TRACES_PROTOCOL -> endpoint.isEmpty() ? null : "http/protobuf";
            default -> null;
        };
    }

    @Override
    public String getName() {
        return "kubemoot-otlp-endpoint";
    }

    @Override
    public int getOrdinal() {
        return ORDINAL;
    }

    private String endpoint() {
        String value = environment.apply(ENV);
        return value == null ? "" : value.trim();
    }

    /**
     * The collector's base URL. The extension adds {@code /v1/traces} itself, so an endpoint
     * that names a path ({@code https://c.example/otlp}) gets the signal path after it.
     */
    static String baseUrl(String endpoint) {
        int end = endpoint.length();
        while (end > 0 && endpoint.charAt(end - 1) == '/') {
            end--;
        }
        return endpoint.substring(0, end);
    }
}
