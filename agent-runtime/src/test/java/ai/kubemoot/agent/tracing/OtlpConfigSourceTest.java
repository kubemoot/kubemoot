package ai.kubemoot.agent.tracing;

import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

class OtlpConfigSourceTest {

    private static OtlpConfigSource with(String endpoint) {
        return new OtlpConfigSource(name -> OtlpConfigSource.ENV.equals(name) ? endpoint : null);
    }

    @Test
    void exportIsOffWithoutAnEndpoint() {
        for (String none : new String[]{null, "", "   "}) {
            var source = with(none);
            assertEquals("true", source.getValue(OtlpConfigSource.SDK_DISABLED));
            assertNull(source.getValue(OtlpConfigSource.TRACES_ENDPOINT));
            assertNull(source.getValue(OtlpConfigSource.TRACES_PROTOCOL));
        }
    }

    @Test
    void anEndpointTurnsTheSdkOnAndExportsOverOtlpHttp() {
        var source = with("http://otel-collector.observability:4318");
        assertEquals("false", source.getValue(OtlpConfigSource.SDK_DISABLED));
        assertEquals("http://otel-collector.observability:4318",
                source.getValue(OtlpConfigSource.TRACES_ENDPOINT));
        assertEquals("http/protobuf", source.getValue(OtlpConfigSource.TRACES_PROTOCOL));
    }

    @Test
    void theExtensionAddsTheSignalPathSoOnlyTheBaseIsGiven() {
        assertEquals("https://c.example/otlp", OtlpConfigSource.baseUrl("https://c.example/otlp/"));
        assertEquals("http://c:4318", OtlpConfigSource.baseUrl("http://c:4318/"));
        assertEquals("http://c:4318", OtlpConfigSource.baseUrl("http://c:4318"));
        assertEquals("http://c:4318", OtlpConfigSource.baseUrl("http://c:4318///"));
    }

    @Test
    void aLowOrdinalLetsExplicitSettingsWin() {
        assertTrue(with("http://c:4318").getOrdinal() < 250);
        assertNull(with("http://c:4318").getValue("something.else"));
        Map<String, String> all = with(null).getProperties();
        assertEquals(Map.of(OtlpConfigSource.SDK_DISABLED, "true"), all);
    }
}
