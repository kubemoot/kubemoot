package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import ai.kubemoot.agent.chat.TextCut;
import ai.kubemoot.agent.logging.LogContent;
import io.nats.client.Connection;
import java.util.HashSet;
import java.util.Set;
import java.util.function.BooleanSupplier;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * What a review shows under a finding, the way a pull request shows the diff: for each call the
 * finding names, the call's stored working (the program, the query, the parameters) and the call's
 * output, read from the workspace store. A reviewer can then comment on the working without first
 * finding and opening it.
 *
 * <p>Only the calls the finding itself names are shown, and each is capped: {@link #WORKING_CHARS}
 * for the working, {@link #OUTPUT_CHARS} and {@link #OUTPUT_LINES} for the output (an output may be
 * the data a call fetched, so only its first lines show), {@link #FINDING_CHARS} for all the calls
 * of one finding and {@link #MESSAGE_CHARS} for one review message. A cut text ends with
 * {@code [cut at N of M chars; full text under <key>]}. A read that fails shows
 * {@link #WORKING_UNAVAILABLE} (or {@link #OUTPUT_UNAVAILABLE}) and never blocks the review.
 *
 * <p>One instance serves one review message: it holds that message's remaining room.
 */
final class ReviewWorking {

    private static final Logger log = LoggerFactory.getLogger(ReviewWorking.class);

    /** The most characters of one call's working shown: a program of about fifty lines. */
    static final int WORKING_CHARS = 1_500;
    /** The most characters of one call's output shown: enough to see its shape and its first rows. */
    static final int OUTPUT_CHARS = 800;
    /** The most lines of one call's output shown. */
    static final int OUTPUT_LINES = 12;
    /** The most characters of working and output shown under one finding: about three calls in full. */
    static final int FINDING_CHARS = 6_000;
    /** The most characters of working and output in one review message: about 6,000 tokens. */
    static final int MESSAGE_CHARS = 24_000;
    /** The most time the reads of one message may take. */
    static final long DEADLINE_NANOS = 5_000_000_000L;

    /** Shown where a call's working could not be read. */
    static final String WORKING_UNAVAILABLE = "[working unavailable]";
    /** Shown where a call's output could not be read. */
    static final String OUTPUT_UNAVAILABLE = "[output unavailable]";

    private static final Pattern WORKING_KEY = Pattern.compile("\\bcall=([^\\]\\s]++)");

    /** One stored object read: its text, and its size in bytes (what the store holds, not what was read). */
    record Stored(String text, long size) {}

    /** Reads the object stored under {@code key} (relative to the thread), at most {@code maxChars}; null when it cannot. */
    @FunctionalInterface
    interface Source {
        Stored read(String key, int maxChars);
    }

    private static final ReviewWorking NONE = new ReviewWorking((key, max) -> null, false);

    private final Source source;
    private final boolean enabled;
    private int left = MESSAGE_CHARS;

    private ReviewWorking(Source source, boolean enabled) {
        this.source = source;
        this.enabled = enabled;
    }

    /** Shows nothing: no bus, so nothing is stored to read. */
    static ReviewWorking none() {
        return NONE;
    }

    /** A message's view of the working, read through {@code source}. */
    static ReviewWorking of(Source source) {
        return new ReviewWorking(source, true);
    }

    /** A message's view of the working stored in the thread under {@code prefix} on {@code conn}. */
    static ReviewWorking of(Connection conn, String prefix) {
        long stop = System.nanoTime() + DEADLINE_NANOS;
        BooleanSupplier timeUp = () -> System.nanoTime() - stop > 0;
        return of((key, maxChars) -> readStored(conn, prefix + key, maxChars, timeUp));
    }

    private static Stored readStored(Connection conn, String fullKey, int maxChars, BooleanSupplier timeUp) {
        try {
            var held = DiscussionArtifacts.storeHolding(conn, fullKey);
            if (held == null) {
                return null;
            }
            var out = new StoredResults.CappedOutput(maxChars * 4, timeUp);
            try {
                held.store().get(fullKey, out);
            } catch (java.io.IOException e) {
                if (out.stopped() == StoredResults.Stop.NONE) {
                    throw e;
                }
            }
            return out.stopped() == StoredResults.Stop.TIME ? null : new Stored(out.text(), held.info().getSize());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return null;
        } catch (Exception e) {
            log.warn("Could not read stored working {} ({}): {}", fullKey, e.getClass().getSimpleName(),
                    LogContent.reason(e));
            return null;
        }
    }

    /**
     * The working and output of the calls {@code finding} names, as lines to follow it; empty when it
     * names none. Each call is shown once in a message.
     */
    String forFinding(String finding) {
        if (!enabled || finding == null || !finding.contains(CallManifest.CALL_PREFIX)) {
            return "";
        }
        var out = new StringBuilder();
        int room = Math.min(FINDING_CHARS, left);
        int omitted = 0;
        Matcher m = CallManifest.CALL_LINE.matcher(finding);
        while (m.find()) {
            if (!shown.add(m.group(1))) {
                continue;
            }
            String block = room < MIN_ROOM_CHARS ? null : callBlock(m.group(1), workingKey(m.group(), m.group(1)));
            if (block == null || block.length() > room) {
                omitted++;
                continue;
            }
            out.append(block);
            room -= block.length();
            left -= block.length();
        }
        return render(out, omitted);
    }

    private static String render(StringBuilder out, int omitted) {
        if (omitted > 0) {
            out.append("[").append(omitted).append(omitted == 1 ? " more call" : " more calls")
                    .append(" not shown here: read them under their keys]\n");
        }
        return out.length() == 0 ? "" : "\n" + out.toString().stripTrailing();
    }

    /** A call is not worth showing in less room than this. */
    private static final int MIN_ROOM_CHARS = 200;

    private final Set<String> shown = new HashSet<>();

    /** The key of the call's working: the call line's own {@code call=} field, null when it says unavailable. */
    private static String workingKey(String callLine, String resultKey) {
        Matcher m = WORKING_KEY.matcher(callLine);
        if (!m.find()) {
            return CallManifest.workingKeyOf(resultKey);
        }
        return CallManifest.WORKING_UNAVAILABLE.equals(m.group(1)) ? null : m.group(1);
    }

    private String callBlock(String resultKey, String workingKey) {
        var sb = new StringBuilder();
        sb.append("[WORKING of ").append(resultKey).append("]\n")
                .append(workingKey == null ? WORKING_UNAVAILABLE : text(workingKey, WORKING_CHARS, Integer.MAX_VALUE,
                        WORKING_UNAVAILABLE))
                .append("\n[OUTPUT of ").append(resultKey).append("]\n")
                .append(text(resultKey, OUTPUT_CHARS, OUTPUT_LINES, OUTPUT_UNAVAILABLE)).append('\n');
        return sb.toString();
    }

    /** The stored text under {@code key}, cut to {@code maxChars} and {@code maxLines} with the cut marker, or {@code unavailable}. */
    private String text(String key, int maxChars, int maxLines, String unavailable) {
        Stored stored = source.read(key, maxChars);
        if (stored == null || stored.text() == null) {
            return unavailable;
        }
        String text = neutral(stored.text());
        String shown = firstLines(TextCut.prefix(text, maxChars), maxLines);
        boolean cut = shown.length() < text.length() || stored.size() > text.getBytes(java.nio.charset.StandardCharsets.UTF_8).length;
        if (!cut) {
            return shown;
        }
        long whole = Math.max(stored.size(), text.length());
        return shown + "\n[cut at " + shown.length() + " of " + whole + " chars; full text under " + key + "]";
    }

    private static String firstLines(String text, int maxLines) {
        int end = 0;
        for (int i = 0; i < maxLines; i++) {
            int nl = text.indexOf('\n', end);
            if (nl < 0) {
                return text;
            }
            end = nl + 1;
        }
        return end >= text.length() ? text : text.substring(0, end - 1);
    }

    /** A working or an output must not pose as the discussion's own lines: a marker of its format is defused. */
    private static String neutral(String text) {
        String out = text;
        for (String marker : CallManifest.RESULT_MARKERS) {
            out = out.replace(marker, "(" + marker.substring(1));
        }
        for (String prefix : CallManifest.CALL_PREFIXES) {
            out = out.replace(prefix, "(" + prefix.substring(1));
        }
        return out;
    }
}
