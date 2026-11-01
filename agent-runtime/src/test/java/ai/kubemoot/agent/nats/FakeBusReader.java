package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * A stream reader over messages a test puts in: per stream, in order, each with the subject
 * it was published to. A filter subject matches the way NATS does ({@code *} one token,
 * {@code >} the rest).
 */
final class FakeBusReader implements BusReader {

    private final Map<String, List<BusMessage>> streams = new HashMap<>();
    private final List<String> reads = new ArrayList<>();

    /** Adds {@code message} to {@code stream}. */
    FakeBusReader put(String stream, BusMessage message) {
        streams.computeIfAbsent(stream, s -> new ArrayList<>()).add(message);
        return this;
    }

    /** Adds a message with the next sequence in {@code stream}. */
    FakeBusReader publish(String stream, String subject, byte[] data) {
        long sequence = streams.computeIfAbsent(stream, s -> new ArrayList<>()).size() + 1L;
        return put(stream, new BusMessage(sequence, subject, data));
    }

    /** The streams and filters read so far, as {@code stream filter}. */
    List<String> reads() {
        return List.copyOf(reads);
    }

    @Override
    public List<BusMessage> readAll(String stream, String filterSubject) {
        reads.add(stream + " " + filterSubject);
        var out = new ArrayList<BusMessage>();
        for (BusMessage message : streams.getOrDefault(stream, List.of())) {
            if (matches(filterSubject, message.subject())) {
                out.add(message);
            }
        }
        return out;
    }

    /** True when {@code subject} matches the NATS subject filter {@code filter}. */
    static boolean matches(String filter, String subject) {
        String[] f = filter.split("\\.");
        String[] s = subject.split("\\.");
        for (int i = 0; i < f.length; i++) {
            if (">".equals(f[i])) {
                return s.length > i;
            }
            if (i >= s.length || (!"*".equals(f[i]) && !f[i].equals(s[i]))) {
                return false;
            }
        }
        return f.length == s.length;
    }
}
