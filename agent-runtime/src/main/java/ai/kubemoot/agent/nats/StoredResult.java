package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.function.Function;
import java.util.regex.Matcher;

/**
 * One stored tool result a tooler's manifest names: its object {@code key}, the {@code tool}, the
 * manifest {@code callLine} it came from, and its whole {@code content}, which is null when the
 * object could not be read.
 */
record StoredResult(String key, String tool, String callLine, String content) {

    boolean readable() {
        return content != null;
    }

    /**
     * The results the call lines in {@code text} name, in order, each read once through {@code read}
     * (null for an object that cannot be read). A key named twice is one result.
     */
    static List<StoredResult> listFrom(String text, Function<String, String> read) {
        var results = new ArrayList<StoredResult>();
        var seen = new HashSet<String>();
        Matcher m = CallManifest.CALL_LINE.matcher(text);
        while (m.find()) {
            if (seen.add(m.group(1))) {
                results.add(new StoredResult(m.group(1), m.group(2), m.group(), read.apply(m.group(1))));
            }
        }
        return results;
    }
}
