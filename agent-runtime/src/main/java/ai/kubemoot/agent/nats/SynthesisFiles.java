package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;

/**
 * The rules the synthesizer follows for a thread whose contributions describe stored files. The
 * synthesizer sees the discussion only: what an agent stated from a file is a fact, and a file
 * nobody stated anything from is data that existed and is unknown. The rules are an ADL resource
 * ({@code prompts/synthesis-files.adl}), not Java text.
 */
final class SynthesisFiles {

    private static final String RULES = load("synthesis-files.adl");

    private SynthesisFiles() {}

    /** The rules, followed by a blank line, when {@code thread} holds file descriptions; otherwise empty. */
    static String rulesFor(String thread) {
        boolean described = thread != null
                && (thread.contains(CallManifest.CALL_PREFIX) || thread.contains(CallManifest.NOT_STORED_PREFIX));
        return described ? RULES + "\n\n" : "";
    }

    private static String load(String name) {
        try (InputStream in = SynthesisFiles.class.getResourceAsStream("/prompts/" + name)) {
            if (in == null) {
                throw new IllegalStateException("missing prompt resource prompts/" + name);
            }
            return new String(in.readAllBytes(), StandardCharsets.UTF_8).strip();
        } catch (IOException e) {
            throw new IllegalStateException("cannot read prompt resource prompts/" + name, e);
        }
    }
}
