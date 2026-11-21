package ai.kubemoot.agent.tracing;

import io.opentelemetry.api.common.AttributeKey;

/** The attribute names the runtime's spans share, written once so they cannot drift apart. */
final class Attrs {

    static final AttributeKey<String> THREAD = AttributeKey.stringKey("kubemoot.thread");
    static final AttributeKey<String> OPERATION = AttributeKey.stringKey("gen_ai.operation.name");

    private Attrs() {
    }

    static String nullToEmpty(String value) {
        return value == null ? "" : value;
    }
}
