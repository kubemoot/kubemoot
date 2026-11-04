package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;

/**
 * Removes the internal lines of an agent's file descriptions from a text. The synthesizer is shown
 * the descriptions without the header and the closing line, which speak to an agent that can read
 * files, and with the results the agents posted; the user-facing answer loses the call lines and
 * the result blocks too, so no internal key reaches it.
 */
final class ManifestText {

    private ManifestText() {}

    /**
     * {@code text} without the manifest's header and closing lines and, when {@code withCalls}, its
     * call lines, not-stored lines and result blocks too.
     */
    static String strip(String text, boolean withCalls) {
        String out = CallManifest.FOOTER_LINE.matcher(CallManifest.HEADER_LINE.matcher(text).replaceAll(""))
                .replaceAll("");
        return withCalls ? withoutCalls(out) : out;
    }

    private static String withoutCalls(String text) {
        String noBlocks = CallManifest.FAILED_RESULT_BLOCK.matcher(
                CallManifest.RESULT_BLOCK.matcher(text).replaceAll("")).replaceAll("");
        return CallManifest.CALL_LINE.matcher(
                CallManifest.NOT_STORED_LINE.matcher(noBlocks).replaceAll("")).replaceAll("");
    }
}
