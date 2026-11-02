package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.util.function.Function;
import java.util.regex.Matcher;

/**
 * Fills a tooler's manifest in with the stored results it names, for the synthesizer. The
 * synthesizer has no tools and cannot read the workspace, so a manifest alone would hand it keys
 * and no data. Each {@code [CALL key=... tool=...]} line becomes {@code [tool]} and the stored result,
 * the form the glued contribution has. A result that cannot be read becomes the same explicit
 * "unavailable" notice a spilled artifact gets, so the synthesizer reports the gap instead of
 * answering over data it never saw. The manifest's own header and closing lines are removed.
 */
final class ManifestInliner {

    private ManifestInliner() {}

    /**
     * {@code text} with every manifest call line replaced by its stored result. {@code read} returns a
     * result's text, or null when it cannot be read. Results are inlined in order until their total
     * passes {@code totalCap} characters; a call past that is replaced by a notice naming its key.
     */
    static String inline(String text, Function<String, String> read, int totalCap) {
        if (text == null || !text.contains(CallManifest.CALL_PREFIX)) {
            return text;
        }
        String body = strip(text, false);
        Matcher m = CallManifest.CALL_LINE.matcher(body);
        var sb = new StringBuilder();
        int used = 0;
        while (m.find()) {
            String key = m.group(1);
            String replacement;
            if (used >= totalCap) {
                replacement = "[" + m.group(2) + "] not inlined, the synthesis input is full; key " + key;
            } else {
                String content = read.apply(key);
                replacement = content == null ? DiscussionOrchestrator.artifactUnavailableNotice(key)
                        : "[" + m.group(2) + "]\n" + content;
                used += replacement.length();
            }
            m.appendReplacement(sb, Matcher.quoteReplacement(replacement));
        }
        m.appendTail(sb);
        return sb.toString();
    }

    /**
     * {@code text} without the manifest's header and closing lines and, when {@code withCalls}, its
     * call lines too. The user-facing answer is stripped of all three, so no internal key reaches it.
     */
    static String strip(String text, boolean withCalls) {
        String out = CallManifest.FOOTER_LINE.matcher(CallManifest.HEADER_LINE.matcher(text).replaceAll(""))
                .replaceAll("");
        return withCalls ? CallManifest.CALL_LINE.matcher(out).replaceAll("") : out;
    }
}
