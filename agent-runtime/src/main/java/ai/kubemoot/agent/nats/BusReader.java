package ai.kubemoot.agent.nats;

import java.io.IOException;
import java.time.Instant;
import java.util.Arrays;
import java.util.List;
import java.util.Objects;

/**
 * Reads what a JetStream stream holds, so a restarted coordinator can see what was
 * written while it was down. Reading does not consume anything.
 */
interface BusReader {

    /** One message of a stream: where it sits, the subject it was published to, and its body. */
    record BusMessage(long sequence, String subject, byte[] data) {
        @Override
        public boolean equals(Object other) {
            return other instanceof BusMessage that
                    && sequence == that.sequence
                    && Objects.equals(subject, that.subject)
                    && Arrays.equals(data, that.data);
        }

        @Override
        public int hashCode() {
            return 31 * Objects.hash(sequence, subject) + Arrays.hashCode(data);
        }

        @Override
        public String toString() {
            return "BusMessage[sequence=" + sequence + ", subject=" + subject + ", data=" + Arrays.toString(data) + "]";
        }
    }

    /**
     * Every message of {@code stream} on {@code filterSubject} (NATS wildcards allowed), oldest
     * first, up to the last one the stream held when the read began.
     */
    List<BusMessage> readAll(String stream, String filterSubject) throws IOException;

    /**
     * The messages of {@code stream} on {@code filterSubject} that were stored at or after
     * {@code since}, oldest first. A reader may return more than that (everything); the caller
     * must not rely on the cut-off, only on getting at least those messages.
     */
    default List<BusMessage> readSince(String stream, String filterSubject, Instant since) throws IOException {
        return readAll(stream, filterSubject);
    }
}
