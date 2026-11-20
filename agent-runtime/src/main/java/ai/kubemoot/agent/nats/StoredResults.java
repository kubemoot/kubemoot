package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.logging.LogContent;
import ai.kubemoot.agent.chat.CallHeaders;
import ai.kubemoot.agent.chat.CallManifest;
import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.DiscussionRoom;
import ai.kubemoot.agent.chat.TextCut;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.OptionalLong;
import java.util.function.BooleanSupplier;
import java.util.function.Supplier;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * The stored results of a thread that holds no fact, read for the synthesizer. When tools ran,
 * succeeded, and no agent stated anything from the result or could post it whole, the files still
 * hold the data; the synthesizer answers from what they cover instead of the coordinator
 * declaring that no data was obtained.
 *
 * <p>The calls come from the thread's catalog ({@link ThreadCatalog}, built from the stored
 * objects' headers, so only objects marked as tool calls and not as errors are read). The size
 * rules are those of any tool result in the discussion ({@link DiscussionRoom}): one result is
 * shown up to one result's share of the room, all of them up to one contribution's share. Each
 * object is read only as far as the room can use it, and the whole read stops at a deadline. A
 * result is shown as the tool returned it; one that is cut says so ({@code complete=false}). A
 * result holding a marker of the discussion's own format is not shown, so its content can never
 * close a block and pose as the discussion.
 */
final class StoredResults {

    private static final Logger log = LoggerFactory.getLogger(StoredResults.class);

    /** Opens one result block. */
    static final String OPEN = "[STORED RESULT ";
    /** Closes it. */
    static final String CLOSE = "[END STORED RESULT]";

    /** A result is not worth showing in less room than this; the rest are counted instead. */
    static final int MIN_SHOWN_CHARS = 200;
    /** The most time the whole read may take. */
    static final long DEADLINE_NANOS = 10_000_000_000L;
    /** The most bytes of a character in UTF-8, to turn the room in characters into bytes to read. */
    private static final int MAX_BYTES_PER_CHAR = 4;

    /** The markers a result must not hold: each would close a block or pose as the discussion's own line. */
    private static final List<String> FORBIDDEN = java.util.stream.Stream.concat(
            java.util.stream.Stream.of(CLOSE, OPEN), CallManifest.RESULT_MARKERS.stream()).toList();

    /** What the read found. */
    enum Status {
        /** Some result is shown. */
        READ,
        /** No successful stored call: no bus, or every call failed or was not stored. */
        NOTHING_STORED,
        /** Results are stored but none could be read. */
        UNREADABLE,
        /** Results were read but none fits the room or may be shown. */
        NO_ROOM
    }

    /** The outcome: the blocks to give the synthesizer when {@code status} is READ, else empty. */
    record Result(Status status, String text) {}

    /** One object read: its text (null when unreadable), and whether the text is less than the object. */
    record Fetched(String text, boolean cut) {}

    /** Reads one call, at most {@code maxBytes}. May throw InterruptedException; returns null when it cannot read. */
    @FunctionalInterface
    interface Reader {
        Fetched read(ThreadCatalog.Entry call, int maxBytes) throws InterruptedException;
    }

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;

    StoredResults(NatsConnectionProvider natsProvider, ChatService chatService) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
    }

    /** Reads the successful stored calls of the thread, from its catalog, as the room allows. */
    Result read(String threadId) {
        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            return new Result(Status.NOTHING_STORED, "");
        }
        String prefix = DiscussionArtifacts.threadPrefix(natsProvider.scope(), threadId);
        var calls = ThreadCatalog.load(conn, prefix).entries().stream().filter(e -> !e.error()).toList();
        OptionalLong window = chatService.simpleCallContextTokens();
        long stop = System.nanoTime() + DEADLINE_NANOS;
        return render(calls, prefix, (call, maxBytes) -> fetch(conn, call, maxBytes, () -> System.nanoTime() - stop > 0),
                DiscussionRoom.perResultChars(window), DiscussionRoom.perContributionChars(window),
                () -> System.nanoTime() - stop > 0);
    }

    private static Fetched fetch(Connection conn, ThreadCatalog.Entry call, int maxBytes, BooleanSupplier timeUp)
            throws InterruptedException {
        try {
            var held = DiscussionArtifacts.storeHolding(conn, call.key());
            if (held == null) {
                log.warn("Could not read stored result {}: no workspace bucket holds it", call.key());
                return null;
            }
            ObjectStore store = held.store();
            ObjectInfo info = held.info();
            boolean markedCut = info != null && info.getHeaders() != null
                    && Boolean.parseBoolean(info.getHeaders().getFirst(CallHeaders.TRUNCATED));
            var out = new CappedOutput(maxBytes, timeUp);
            try {
                store.get(call.key(), out);
            } catch (IOException e) {
                if (out.stopped() == Stop.NONE) {
                    throw e;
                }
                // the room is full or time is up: the output aborted the read on purpose
            }
            return out.stopped() == Stop.TIME ? null : new Fetched(out.text(), markedCut || out.stopped() == Stop.CAP);
        } catch (InterruptedException e) {
            throw e;
        } catch (Exception e) {
            log.warn("Could not read stored result {} ({}): {}", call.key(), e.getClass().getSimpleName(), LogContent.reason(e));
            return null;
        }
    }

    /**
     * The blocks for {@code calls}, each result shown up to {@code perResult} characters and all of
     * them up to {@code total}. {@code timeUp} ends the read; an interrupt ends it too and is kept
     * on the thread.
     */
    static Result render(List<ThreadCatalog.Entry> calls, String prefix, Reader reader, int perResult, int total,
                         BooleanSupplier timeUp) {
        if (calls.isEmpty()) {
            return new Result(Status.NOTHING_STORED, "");
        }
        var tally = new Tally(total);
        int maxBytes = (int) Math.min(Integer.MAX_VALUE - 8L, (long) perResult * MAX_BYTES_PER_CHAR);
        for (var call : calls) {
            tally.add(call, prefix, Math.min(perResult, tally.left), () -> readOne(reader, call, maxBytes, timeUp));
            if (tally.unreadable > 0 && (Thread.currentThread().isInterrupted() || timeUp.getAsBoolean())) {
                break;
            }
        }
        return tally.result();
    }

    /** What the read of the calls has produced so far. */
    private static final class Tally {
        final StringBuilder out = new StringBuilder();
        int left;
        int shown;
        int notShown;
        int unreadable;

        Tally(int total) {
            this.left = total;
        }

        void add(ThreadCatalog.Entry call, String prefix, int room, Supplier<Fetched> read) {
            if (room < MIN_SHOWN_CHARS) {
                notShown++;
                return;
            }
            Fetched fetched = read.get();
            if (fetched == null || fetched.text() == null || fetched.text().isBlank()) {
                unreadable++;
                return;
            }
            String block = block(call, prefix, fetched, room);
            if (block == null) {
                notShown++;
                return;
            }
            out.append(block).append('\n');
            left -= block.length();
            shown++;
        }

        Result result() {
            if (shown == 0) {
                return new Result(unreadable > 0 ? Status.UNREADABLE : Status.NO_ROOM, "");
            }
            if (notShown > 0) {
                out.append("[").append(notShown).append(notShown == 1 ? " more stored result" : " more stored results")
                        .append(" not shown: no room left in the discussion, or the content could not be shown]\n");
            }
            return new Result(Status.READ, out.toString());
        }
    }

    private static Fetched readOne(Reader reader, ThreadCatalog.Entry call, int maxBytes, BooleanSupplier timeUp) {
        if (timeUp.getAsBoolean() || Thread.currentThread().isInterrupted()) {
            return null;
        }
        try {
            return reader.read(call, maxBytes);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return null;
        } catch (RuntimeException e) {
            log.warn("Could not read stored result {} ({}): {}", call.key(), e.getClass().getSimpleName(), LogContent.reason(e));
            return null;
        }
    }

    /** The block showing one result, or null when its text holds a marker of the discussion's format. */
    private static String block(ThreadCatalog.Entry call, String prefix, Fetched fetched, int room) {
        String shown = TextCut.prefix(fetched.text(), room);
        if (FORBIDDEN.stream().anyMatch(shown::contains)) {
            return null;
        }
        boolean complete = !fetched.cut() && shown.length() == fetched.text().length()
                && !fetched.text().endsWith(TextCut.MARKER);
        return OPEN + "key=" + call.relativeKey(prefix) + " tool=" + call.tool() + " type=" + call.type()
                + " bytes=" + call.bytes() + " shown=" + shown.length() + " complete=" + complete + "]\n"
                + shown + "\n" + CLOSE;
    }

    /** Why a read of one object stopped before its end. */
    enum Stop { NONE, CAP, TIME }

    /** Thrown by the output when it holds all the room can use, so the read of the object is abandoned. */
    private static final class CapReached extends IOException {
        CapReached() {
            super("stored result larger than the room", null);
        }
    }

    /** Keeps the first bytes written; past the cap, or past the deadline, it aborts the read. */
    static final class CappedOutput extends OutputStream {
        private final ByteArrayOutputStream kept = new ByteArrayOutputStream();
        private final int max;
        private final BooleanSupplier timeUp;
        private Stop stopped = Stop.NONE;

        CappedOutput(int max, BooleanSupplier timeUp) {
            this.max = max;
            this.timeUp = timeUp;
        }

        @Override
        public void write(int b) throws IOException {
            write(new byte[] {(byte) b}, 0, 1);
        }

        @Override
        public void write(byte[] b, int off, int len) throws IOException {
            if (timeUp.getAsBoolean()) {
                stopped = Stop.TIME;
                throw new CapReached();
            }
            int room = max - kept.size();
            kept.write(b, off, Math.min(len, room));
            if (len > room) {
                stopped = Stop.CAP;
                throw new CapReached();
            }
        }

        Stop stopped() {
            return stopped;
        }

        /** The kept bytes as text; when the read was cut, an incomplete character at the end is left out. */
        String text() {
            byte[] bytes = kept.toByteArray();
            int end = stopped == Stop.NONE ? bytes.length : completeEnd(bytes);
            return new String(bytes, 0, end, StandardCharsets.UTF_8);
        }

        private static int completeEnd(byte[] b) {
            int i = b.length - 1;
            while (i >= 0 && (b[i] & 0xC0) == 0x80 && b.length - i < 4) {
                i--;
            }
            if (i < 0 || (b[i] & 0x80) == 0) {
                return b.length;
            }
            int need = (b[i] & 0xE0) == 0xC0 ? 2 : (b[i] & 0xF0) == 0xE0 ? 3 : 4;
            return b.length - i < need ? i : b.length;
        }
    }
}
