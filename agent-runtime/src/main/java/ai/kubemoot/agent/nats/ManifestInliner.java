package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;

/**
 * The text helpers a synthesis input needs around a tooler's manifest. The stored results the
 * manifest names are delivered by {@link EvidenceSynthesis}; this class cuts a text at a fixed
 * length (a spilled artifact, or a result when the window is unknown) and removes the manifest's own
 * header and closing lines.
 */
final class ManifestInliner {

    /** What a result cut at the fixed character limit ends with (the limit applies when the window is unknown). */
    static final String CUT_NOTE = "\n[...artifact truncated for synthesis...]";

    private ManifestInliner() {}

    /**
     * {@code content} as it is, or cut to {@code max} characters and ended with {@link #CUT_NOTE}.
     * The one fixed-length cut a synthesis input makes.
     */
    static String cut(String content, int max) {
        return content.length() > max ? content.substring(0, max) + CUT_NOTE : content;
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
