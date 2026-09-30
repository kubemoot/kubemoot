package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** A concurrence reply is a verdict: CONCUR:, CONCERN:, or none. */
class ConcurrenceReplyTest {

    @Test
    void concur_carriesTheTextAfterTheSentinel() {
        var r = ConcurrenceReply.classify("CONCUR: the namespaces match the cluster.");
        assertEquals(ConcurrenceReply.Verdict.CONCUR, r.verdict());
        assertEquals("the namespaces match the cluster.", r.text());
    }

    @Test
    void bareConcur_carriesTheStandardText() {
        var r = ConcurrenceReply.classify("  CONCUR:  ");
        assertEquals(ConcurrenceReply.Verdict.CONCUR, r.verdict());
        assertEquals(ConcurrenceReply.CONCURS, r.text());
    }

    @Test
    void concern_carriesWhatIsWrong() {
        var r = ConcurrenceReply.classify("CONCERN: the list stops at 22 of 141 deployments");
        assertEquals(ConcurrenceReply.Verdict.CONCERN, r.verdict());
        assertEquals("the list stops at 22 of 141 deployments", r.text());
    }

    @Test
    void toolGap_isAConcernAsOnTheContributionPath() {
        assertEquals(ConcurrenceReply.Verdict.CONCERN, ConcurrenceReply.classify("TOOL_GAP: no CNI tool").verdict());
    }

    @Test
    void aFreeFormAnswer_isNoVerdict() {
        var r = ConcurrenceReply.classify("The etcd leader runs on homelab-k8s-1-cp; disk write is 1.68 MB/s.");
        assertEquals(ConcurrenceReply.Verdict.NONE, r.verdict());
        assertEquals("The etcd leader runs on homelab-k8s-1-cp; disk write is 1.68 MB/s.", r.text());
    }

    @Test
    void theSentinelIsCaseSensitive_andMustOpenTheReply() {
        assertEquals(ConcurrenceReply.Verdict.NONE, ConcurrenceReply.classify("Concur: fine").verdict());
        assertEquals(ConcurrenceReply.Verdict.NONE, ConcurrenceReply.classify("I CONCUR: fine").verdict());
        assertEquals(ConcurrenceReply.Verdict.NONE, ConcurrenceReply.classify("NOTHING_TO_ADD").verdict());
    }

    @Test
    void emptyOrNull_isEmpty() {
        assertEquals(ConcurrenceReply.Verdict.EMPTY, ConcurrenceReply.classify(null).verdict());
        assertEquals(ConcurrenceReply.Verdict.EMPTY, ConcurrenceReply.classify(" \n ").verdict());
    }
}
