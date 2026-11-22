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

    private static final String RATIFIER_INSTRUCTION = String.join("\n",
            "[RATIFY]",
            "ASSERT the draft above is the crew's answer; you ratify it against what you found.",
            "ASSERT check each name, count and status in the draft against your own results before you answer.",
            "WHEN the draft states a name, count or status your results do not hold THEN reply CONCERN and name the statement.",
            "WHEN the draft contradicts your results THEN reply BLOCK and say what your results show.",
            "WHEN every statement of the draft that your results cover is right THEN reply AGREE.",
            "ASSERT the first word of your reply is AGREE, CONCERN or BLOCK; the rest is your reason.",
            "NEVER agree to a statement you have not checked against a result.");

    private static final String REVISION_RULES = String.join("\n",
            "ASSERT the objections come from agents that checked the draft against what they found.",
            "WHEN an objection shows a statement of the draft is not in the discussion THEN correct it from the discussion or remove it.",
            "WHEN an objection is wrong about the discussion THEN keep the statement.",
            "ASSERT write the whole revised answer and nothing else.");

    private RatifyText() {}

    /** What a ratifier is asked after the thread, which already ends with the draft. */
    static String ratifierInstruction() {
        return RATIFIER_INSTRUCTION;
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
