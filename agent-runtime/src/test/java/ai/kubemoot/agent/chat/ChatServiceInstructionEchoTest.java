package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link ChatService#isInstructionEcho(String)} — the guard that
 * stops a recovery-turn response that merely parrots an instruction prompt from
 * being published as an agent's contribution (the "DEFINE COMPONENT
 * final-answer-recovery" leak found 2026-06-12).
 */
class ChatServiceInstructionEchoTest {

    @Test
    void detectsAdlRecoveryEcho() {
        String echo = "DEFINE COMPONENT final-answer-recovery\n"
                + "WHEN tool results are present above AND no final answer was written\n"
                + "THEN write the final answer now";
        assertTrue(ChatService.isInstructionEcho(echo));
    }

    @Test
    void detectsPlainRecoveryEcho() {
        assertTrue(ChatService.isInstructionEcho(
                "Write the final answer to the user's question now, using only the tool results shown above."));
    }

    @Test
    void detectsEchoedAgentAdlSystemPrompt() {
        assertTrue(ChatService.isInstructionEcho(
                "DEFINE COMPONENT k8s-nodes-system\nWHEN asked about nodes THEN list them ASSERT cluster-wide"));
    }

    @Test
    void allowsGenuineAnswer() {
        assertFalse(ChatService.isInstructionEcho(
                "The Proxmox cluster has 3 running VMs: k8s-cp (vmid 150), worker-1 (151), nfs-storage (120)."));
        assertFalse(ChatService.isInstructionEcho(
                "The control plane nodes are not an appropriate target for general workloads."));
    }

    @Test
    void handlesNullAndBlank() {
        assertFalse(ChatService.isInstructionEcho(null));
        assertFalse(ChatService.isInstructionEcho("   "));
    }
}
