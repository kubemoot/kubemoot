package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.DiscussionRoom;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import java.io.ByteArrayOutputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.OptionalLong;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * The stored results of a thread that holds no fact, read for the synthesizer. When tools ran,
 * succeeded, and no agent stated anything from the result or could post it whole, the files still
 * hold the data; the synthesizer answers from what they cover instead of the coordinator
 * declaring that no data was obtained.
 *
 * <p>The size rules are those of any tool result in the discussion ({@link DiscussionRoom}): one
 * result is shown up to one result's share of the room, all of them up to one contribution's
 * share. A result that is longer is cut and says so; a result left out for lack of room is counted.
 * JSON is shown compactly (no indentation) so the room holds more of it.
 */
final class StoredResults {

    private static final Logger log = LoggerFactory.getLogger(StoredResults.class);
    private static final ObjectMapper MAPPER = new ObjectMapper();

    /** Opens one result block. */
    static final String OPEN = "[STORED RESULT ";
    /** Closes it. */
    static final String CLOSE = "[END STORED RESULT]";

    /** The most bytes of one stored object read; the runtime never stores more than this many characters. */
    private static final int MAX_READ_BYTES = 4_000_000;
    /** A result is not worth showing in less room than this; the rest are counted instead. */
    private static final int MIN_SHOWN_CHARS = 200;

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;

    StoredResults(NatsConnectionProvider natsProvider, ChatService chatService) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
    }

    /** Reads the text of the object stored under a full key, or null when it cannot be read. */
    @FunctionalInterface
    interface Reader {
        String read(String fullKey);
    }

    /**
     * The blocks that show {@code calls}, or an empty string when none could be read (no bus, no
     * object). Each call is read from the thread's own prefix in the discussion bucket.
     */
    String read(String threadId, List<DiscussionFacts.StoredCall> calls) {
        Connection conn = natsProvider.getConnection();
        if (conn == null || calls.isEmpty()) {
            return "";
        }
        String prefix = DiscussionArtifacts.threadPrefix(natsProvider.scope(), threadId);
        OptionalLong window = chatService.simpleCallContextTokens();
        return render(calls, key -> fetch(conn, prefix + key), DiscussionRoom.perResultChars(window),
                DiscussionRoom.perContributionChars(window));
    }

    private static String fetch(Connection conn, String fullKey) {
        try {
            var out = new CappedOutput(MAX_READ_BYTES);
            conn.objectStore(DiscussionArtifacts.BUCKET).get(fullKey, out);
            return out.text();
        } catch (Exception e) {
            log.warn("Could not read stored result {} ({}): {}", fullKey, e.getClass().getSimpleName(), e.getMessage());
            return null;
        }
    }

    /**
     * The blocks for {@code calls}, each result shown up to {@code perResult} characters and all of
     * them up to {@code total}. Empty when no result could be read.
     */
    static String render(List<DiscussionFacts.StoredCall> calls, Reader reader, int perResult, int total) {
        var out = new StringBuilder();
        int left = total;
        int notShown = 0;
        int shownCount = 0;
        for (var call : calls) {
            int room = Math.min(perResult, left);
            if (room < MIN_SHOWN_CHARS) {
                notShown++;
                continue;
            }
            String text = compact(reader.read(call.key()));
            if (text == null) {
                continue;
            }
            String block = block(call, text, room);
            out.append(block).append('\n');
            left -= block.length();
            shownCount++;
        }
        if (shownCount == 0) {
            return "";
        }
        if (notShown > 0) {
            out.append("[").append(notShown).append(" more stored results not shown: no room left in the discussion]\n");
        }
        return out.toString();
    }

    private static String block(DiscussionFacts.StoredCall call, String text, int room) {
        boolean complete = text.length() <= room;
        String shown = complete ? text : text.substring(0, room);
        return OPEN + "key=" + call.key() + " tool=" + call.tool() + " chars=" + text.length()
                + " shown=" + shown.length() + " complete=" + complete + "]\n" + shown + "\n" + CLOSE;
    }

    /** The text with the indentation of a JSON document removed; the text as it is when it is not JSON; null for null or blank. */
    static String compact(String text) {
        if (text == null || text.isBlank()) {
            return null;
        }
        String trimmed = text.strip();
        if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
            try {
                JsonNode tree = MAPPER.readTree(trimmed);
                return MAPPER.writeValueAsString(tree);
            } catch (Exception e) {
                return trimmed;
            }
        }
        return trimmed;
    }

    /** Keeps the first bytes written and drops the rest, so a large object never fills memory. */
    private static final class CappedOutput extends OutputStream {
        private final ByteArrayOutputStream kept = new ByteArrayOutputStream();
        private final int max;

        CappedOutput(int max) {
            this.max = max;
        }

        @Override
        public void write(int b) {
            if (kept.size() < max) {
                kept.write(b);
            }
        }

        @Override
        public void write(byte[] b, int off, int len) {
            kept.write(b, off, Math.min(len, Math.max(0, max - kept.size())));
        }

        String text() {
            return kept.toString(StandardCharsets.UTF_8);
        }
    }
}
