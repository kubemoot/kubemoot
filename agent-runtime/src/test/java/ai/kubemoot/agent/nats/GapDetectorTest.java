package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

class GapDetectorTest {

    @Test
    void storeAndFind() {
        var detector = new GapDetector();
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq");
        var result = detector.findProposal("I need help with rabbitmq");
        assertNotNull(result);
        assertEquals("rabbitmq", result.domain());
        assertEquals("rabbitmq-server", result.serverName());
    }

    @Test
    void findCaseInsensitive() {
        var detector = new GapDetector();
        detector.storeProposal("RabbitMQ", "rabbitmq-server", "docker.io/rabbitmq");
        var result = detector.findProposal("rabbitmq cluster setup");
        assertNotNull(result);
    }

    @Test
    void findNoMatch() {
        var detector = new GapDetector();
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq");
        assertNull(detector.findProposal("kubernetes pods"));
    }

    @Test
    void storeWithDocUrls() {
        var detector = new GapDetector();
        var urls = List.of("https://rabbitmq.com/docs", "https://example.com");
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq", urls);
        var result = detector.findProposal("rabbitmq");
        assertNotNull(result);
        assertEquals(2, result.docUrls().size());
        assertEquals("https://rabbitmq.com/docs", result.docUrls().get(0));
    }

    @Test
    void storeWithNullDocUrls() {
        var detector = new GapDetector();
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq", null);
        var result = detector.findProposal("rabbitmq");
        assertNotNull(result);
        assertNotNull(result.docUrls());
        assertTrue(result.docUrls().isEmpty());
    }

    @Test
    void removeProposal() {
        var detector = new GapDetector();
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq");
        detector.removeProposal("rabbitmq");
        assertNull(detector.findProposal("rabbitmq"));
    }

    @Test
    void storeOverwrites() {
        var detector = new GapDetector();
        detector.storeProposal("rabbitmq", "old-server", "old-url");
        detector.storeProposal("rabbitmq", "new-server", "new-url");
        var result = detector.findProposal("rabbitmq");
        assertNotNull(result);
        assertEquals("new-server", result.serverName());
        assertEquals("new-url", result.registryUrl());
    }

    @Test
    void legacyConstructorBehavesLikeDefault() {
        var detector = new GapDetector(120);
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq");
        var result = detector.findProposal("rabbitmq");
        assertNotNull(result);
        assertEquals("rabbitmq-server", result.serverName());
    }

    @Test
    void cleanupFreshEntry() {
        var detector = new GapDetector();
        detector.storeProposal("rabbitmq", "rabbitmq-server", "docker.io/rabbitmq");
        detector.cleanup(); // should NOT remove — entry is < 3600s old
        assertNotNull(detector.findProposal("rabbitmq"));
    }
}
