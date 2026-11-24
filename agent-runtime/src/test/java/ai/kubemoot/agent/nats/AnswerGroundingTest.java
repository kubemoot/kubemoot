package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.List;
import org.junit.jupiter.api.Test;

class AnswerGroundingTest {

    /** The answer of conversation c6ce88c0: names no tool returned, for a cluster of 14. */
    private static final String INVENTED = "All 15 cluster nodes are healthy.\n- crew-node-01: Ready\n"
            + "- crew-node-02: Ready\n- crew-node-10: Ready\n- *(5 additional nodes not shown)*";

    private static final String DISCUSSION = "Are all the cluster nodes healthy right now?\n"
            + "[CALL key=k8s-nodes/001 tool=resources_list rows=14 bytes=41928]\n"
            + "[CALL key=compute/001 tool=workspace_query rows=10 bytes=30881]";

    @Test
    void namesIn_findsInstanceNamesOnce() {
        assertEquals(List.of("crew-node-01", "crew-node-02", "crew-node-10"), AnswerGrounding.namesIn(INVENTED));
        assertEquals(List.of("homelab-k8s-1-cp", "worker-3"),
                AnswerGrounding.namesIn("homelab-k8s-1-cp, worker-3 and homelab-k8s-1-cp again"));
    }

    @Test
    void namesIn_ignoresPlainHyphenatedWordsPathsAndHosts() {
        assertTrue(AnswerGrounding.namesIn("read-only kube-system control-plane well-known").isEmpty());
        assertTrue(AnswerGrounding.namesIn("/kubemoot/workspace/node-01/x and host.node-01.example").isEmpty());
        assertTrue(AnswerGrounding.namesIn("").isEmpty());
        assertTrue(AnswerGrounding.namesIn(null).isEmpty());
    }

    @Test
    void ungrounded_flagsTheInventedNamesWhenOnlyDescriptionsWereSeen() {
        assertEquals(List.of("crew-node-01", "crew-node-02", "crew-node-10"),
                AnswerGrounding.ungrounded(INVENTED, List.of(DISCUSSION)));
    }

    @Test
    void ungrounded_acceptsNamesTheDiscussionOrAStoredResultHolds() {
        String stored = "NAME STATUS\nhomelab-k8s-1-cp Ready\nHomelab-K8s-1-Worker-3 NotReady";
        String draft = "homelab-k8s-1-cp is Ready; homelab-k8s-1-worker-3 is NotReady; crew-node-01 is Ready";
        assertEquals(List.of("crew-node-01"), AnswerGrounding.ungrounded(draft, List.of(DISCUSSION, stored)));
        assertTrue(AnswerGrounding.ungrounded("homelab-k8s-1-cp", List.of(DISCUSSION, stored)).isEmpty());
    }

    @Test
    void ungrounded_acceptsAShortFormInsideAGroundedName() {
        assertTrue(AnswerGrounding.ungrounded("the nodes -cp-2 and cp-3", List.of("homelab-k8s-1-cp-2 homelab-k8s-1-cp-3")).isEmpty());
    }

    @Test
    void ungrounded_withNoSourcesFlagsEverything() {
        assertEquals(3, AnswerGrounding.ungrounded(INVENTED, List.of()).size());
        assertEquals(1, AnswerGrounding.ungrounded("worker-3", java.util.Arrays.asList((String) null)).size());
    }

    @Test
    void reprompt_namesTheMissingNamesAndKeepsTheDraft() {
        String text = AnswerGrounding.reprompt("CONV", "DRAFT", List.of("crew-node-01"));
        assertTrue(text.startsWith("CONV"));
        assertTrue(text.contains("YOUR DRAFT ANSWER:\nDRAFT"));
        assertTrue(text.contains("crew-node-01"));
    }

    @Test
    void caveat_isPlainAndListsAtMostTheLimit() {
        assertTrue(AnswerGrounding.caveat(List.of("a-1")).contains("this name could not be matched"));
        List<String> many = java.util.stream.IntStream.range(0, 14).mapToObj(i -> "node-" + i).toList();
        String text = AnswerGrounding.caveat(many);
        assertTrue(text.contains("these names") && text.contains("and 4 more"), text);
        assertTrue(text.contains("node-9") && !text.contains("node-10"), text);
    }
}
