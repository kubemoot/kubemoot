package ai.kubemoot.agent.nats;

import java.io.IOException;
import java.util.List;

/**
 * Reads what a JetStream stream holds, so a restarted coordinator can see what was
 * written while it was down. Reading does not consume anything.
 */
interface BusReader {

    /** One message of a stream: where it sits, the subject it was published to, and its body. */
    record BusMessage(long sequence, String subject, byte[] data) {
    }

    /**
     * Every message of {@code stream} on {@code filterSubject} (NATS wildcards allowed), oldest
     * first, up to the last one the stream held when the read began.
     */
    List<BusMessage> readAll(String stream, String filterSubject) throws IOException;
}
