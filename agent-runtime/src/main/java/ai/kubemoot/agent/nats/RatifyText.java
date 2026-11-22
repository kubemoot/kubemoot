package ai.kubemoot.agent.nats;

import java.util.List;

/**
 * The words of the ratification protocol, built in code: what a ratifier is asked, and what the
 * coordinator is asked when it revises. They state the contract (check the draft against what
 * you found, open the reply with the verdict) and name no tool: an agent chooses its tools by
 * their own descriptions.
 */
final class RatifyText {

    /** The message type of the coordinator's draft. */
    static final String MSG_DRAFT = "draft";

    /** The metadata key a ratifier's signal carries: the round it answers. */
    static final String FIELD_RATIFIES = "ratifies";

    /** The metadata key that names a draft, on the draft and on every reply to it. */
    static final String FIELD_DRAFT_ID = "draftId";

    /** The marker that ends the message a ratifier is asked, and that its ratify-prompt module speaks to. */
    static final String RATIFY_MARKER = "[RATIFY]";

    /** The name of the PromptModule that carries the ratifier's instructions in a crew's chart. */
    static final String PROMPT_MODULE = "ratify-prompt";

    /** The line a composed {@value #PROMPT_MODULE} module opens with: what the runtime looks for in an agent's prompt. */
    static final String MODULE_SENTINEL = "DEFINE COMPONENT " + PROMPT_MODULE;

    /**
     * What a ratifier is told when its crew ships no {@value #PROMPT_MODULE} module: the least the
     * protocol needs (the verdict word first, a check against its own results). The crew's module
     * is the full instruction and the one to read.
     */
    private static final String DEFAULT_INSTRUCTION = String.join("\n",
            RATIFY_MARKER,
            "ASSERT check each name, count and status in the draft above against your own results before you answer.",
            "ASSERT the first word of your reply is AGREE, CONCERN or BLOCK; the rest is your reason.");

    private static final String REVISION_RULES = String.join("\n",
            "ASSERT the objections come from agents that checked the draft against what they found.",
            "WHEN an objection shows a statement of the draft is not in the discussion THEN correct it from the discussion or remove it.",
            "WHEN an objection is wrong about the discussion THEN keep the statement.",
            "ASSERT write the whole revised answer and nothing else.");

    private RatifyText() {}

    /**
     * What a ratifier is asked after the thread, which already ends with the draft: the marker
     * alone when its prompt carries the crew's {@value #PROMPT_MODULE} module (the crew's wording
     * governs), else the built-in default.
     */
    static String ratifierInstruction(boolean promptHasModule) {
        return promptHasModule ? RATIFY_MARKER : DEFAULT_INSTRUCTION;
    }

    /** The coordinator's revision prompt: the evidence, the draft, and the objections to it. */
    static String revisionPrompt(String evidence, String draft, List<RatifyState.Verdict> objections) {
        StringBuilder out = new StringBuilder(evidence == null ? "" : evidence);
        out.append("\n\n[DRAFT]\n").append(draft).append("\n\n[OBJECTIONS]\n");
        objections.forEach(v -> out.append("- ").append(v.agent()).append(" (").append(v.kind().signal())
                .append("): ").append(v.text().isEmpty() ? "no reason given" : v.text()).append('\n'));
        return out.append('\n').append(REVISION_RULES).toString();
    }
}
