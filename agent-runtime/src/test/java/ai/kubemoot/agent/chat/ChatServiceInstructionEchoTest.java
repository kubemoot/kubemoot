package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link ChatService#isInstructionEcho(String, String)}: the guard that
 * stops an answer which merely parrots the agent's instructions from being published as
 * its contribution, without dropping answers that quote ADL from another document.
 */
class ChatServiceInstructionEchoTest {

    private static final String OWN_PROMPT = """
            DEFINE DOMAIN structure-checker
            DESCRIPTION Checks the structure of an ADL specification
            DEFINE COMPONENT procedure
            WHEN a DEFINE line is not followed directly by a DESCRIPTION line THEN report it as a missing DESCRIPTION
            WHEN an ASSERT names a component THEN check that a DEFINE COMPONENT with that exact name exists
            NEVER praise, summarise, or suggest a redesign
            """;

    @Test
    void detectsAnEchoOfTheAgentsOwnPrompt() {
        String echo = """
                DEFINE COMPONENT procedure
                WHEN a DEFINE line is not followed directly by a DESCRIPTION line THEN report it as a missing DESCRIPTION
                WHEN an ASSERT names a component THEN check that a DEFINE COMPONENT with that exact name exists
                """;
        assertTrue(ChatService.isInstructionEcho(echo, OWN_PROMPT));
    }

    @Test
    void detectsPlainRecoveryEcho() {
        assertTrue(ChatService.isInstructionEcho(
                "Write the final answer to the user's question now, using only the tool results shown above.", OWN_PROMPT));
    }

    @Test
    void allowsAReviewThatQuotesAnotherSpecificationsAdl() {
        String review = """
                1. Missing DESCRIPTION: DEFINE COMPONENT warehouse-adapter has no DESCRIPTION line.
                2. Unenforceable: ASSERT inventory-service reserves stock before an order is paid names an undefined component.
                3. Contradiction in order-service: NEVER call the payment-provider directly
                   contradicts WHEN a card is declined THEN call the payment-provider to retry once.
                """;
        assertFalse(ChatService.isInstructionEcho(review, OWN_PROMPT));
    }

    @Test
    void allowsGenuineAnswer() {
        assertFalse(ChatService.isInstructionEcho(
                "The Proxmox cluster has 3 running VMs: k8s-cp (vmid 150), worker-1 (151), nfs-storage (120).", OWN_PROMPT));
        assertFalse(ChatService.isInstructionEcho(
                "The control plane nodes are not an appropriate target for general workloads.", ""));
    }

    @Test
    void anAnswerWithOneCopiedLineAmongManyIsNotAnEcho() {
        String answer = """
                The specification has one problem worth reporting here.
                NEVER praise, summarise, or suggest a redesign
                warehouse-adapter is defined without a DESCRIPTION line.
                Everything else follows the checklist as written.
                """;
        assertFalse(ChatService.isInstructionEcho(answer, OWN_PROMPT));
    }

    @Test
    void handlesNullAndBlank() {
        assertFalse(ChatService.isInstructionEcho(null, OWN_PROMPT));
        assertFalse(ChatService.isInstructionEcho("   ", OWN_PROMPT));
        assertFalse(ChatService.isInstructionEcho("an answer", null));
    }
}
