package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;

/**
 * What one synthesis did with the stored results: the window and room of each round, how many
 * results reached the model, how many bytes, the rounds used, the parts a split result made, and the
 * keys that were not delivered. It is logged as one line and its rounds and bytes go in the
 * coordinator's synthesis event, so a measurement can read them.
 */
final class EvidenceReport {

    /** Value in the window and room lists for a round whose window was unknown. */
    static final long UNKNOWN = -1;

    private final int results;
    private final List<Long> windows = new ArrayList<>();
    private final List<Long> rooms = new ArrayList<>();
    private final List<String> notDelivered = new ArrayList<>();
    private int rounds;
    private int chunks;
    private int filled;
    private long filledBytes;

    EvidenceReport(int results) {
        this.results = results;
    }

    /** A round's call was made. */
    void roundStarted(long window, long room) {
        rounds++;
        windows.add(window);
        rooms.add(room);
    }

    /** {@code bytes} of a stored result reached the model, as a part when {@code part}. */
    void delivered(long bytes, boolean part) {
        filledBytes += bytes;
        if (part) {
            chunks++;
        }
    }

    /** The synthesis ended with {@code filled} results fully delivered and these keys not. */
    void finish(int filled, List<String> keysNotDelivered) {
        this.filled = filled;
        notDelivered.addAll(keysNotDelivered);
    }

    int rounds() {
        return rounds;
    }

    long filledBytes() {
        return filledBytes;
    }

    int chunks() {
        return chunks;
    }

    int filled() {
        return filled;
    }

    List<String> notDelivered() {
        return List.copyOf(notDelivered);
    }

    List<Long> windows() {
        return List.copyOf(windows);
    }

    List<Long> rooms() {
        return List.copyOf(rooms);
    }

    /** The one log line of a synthesis. */
    String logLine(String threadId) {
        return "synthesis evidence: thread=" + threadId + " windows=" + show(windows) + " rooms=" + show(rooms)
                + " results=" + results + " filled=" + filled + " rounds=" + rounds + " chunks=" + chunks
                + " bytes=" + filledBytes + " notDelivered=" + notDelivered;
    }

    private static String show(List<Long> values) {
        return values.stream().map(v -> v == UNKNOWN ? "unknown" : Long.toString(v)).toList().toString();
    }
}
