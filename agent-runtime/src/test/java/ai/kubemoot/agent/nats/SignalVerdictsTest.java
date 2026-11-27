package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.signal.SignalCall;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SignalVerdictsTest {

    private static final String FILES = "[CALL key=ns/crew/t1/a/001-resources_list tool=resources_list]";
    private static final SignalVerdicts.Finding FINDING = new SignalVerdicts.Finding("7 nodes, all Ready", FILES);

    private static SignalVerdicts.Verdict of(String kind, String to, String statement) {
        return SignalVerdicts.of(new SignalCall(kind, to, statement, "", ""), FINDING, true);
    }

    @Test
    void anAgreePostsTheFindingAndNamesTheCall() {
        var v = SignalVerdicts.of(new SignalCall("agree", "", "7 nodes", "from the node list", "k1"), FINDING, true);
        assertEquals("agree", v.signal());
        assertEquals("7 nodes, all Ready\n\n" + FILES, v.content(), "the reply stays the finding");
        assertEquals("agree", v.metadata().get(SignalVerdicts.FIELD_KIND));
        assertEquals("7 nodes", v.metadata().get(SignalVerdicts.FIELD_STATEMENT));
        assertEquals("from the node list", v.metadata().get(SignalVerdicts.FIELD_REASON));
        assertEquals("k1", v.metadata().get(SignalVerdicts.FIELD_EVIDENCE));
        assertFalse(v.metadata().containsKey(SignalVerdicts.FIELD_TO));
    }

    @Test
    void aConcernAndABlockLeadWithTheirStatementThenTheFinding() {
        var concern = of("concern", "", "k8s-nodes counted 100 nodes");
        assertEquals("concern", concern.signal());
        assertTrue(concern.content().startsWith("k8s-nodes counted 100 nodes\n\n7 nodes, all Ready"));
        assertTrue(concern.content().endsWith(FILES));
        assertEquals("block", of("block", "", "it contradicts my results").signal());
    }

    @Test
    void aQuestionIsPublishedAsAQuestionToTheAgentItNames() {
        var v = of("question", "k8s-nodes", "can you recheck the node count?");
        assertEquals(SignalVerdicts.MSG_QUESTION, v.signal());
        assertEquals("k8s-nodes", v.metadata().get(SignalVerdicts.FIELD_TO));
        assertTrue(v.content().startsWith("can you recheck the node count?"));
    }

    @Test
    void aToolGapIsAConcernWithTheNeedAloneAndAStandAsideCarriesNoFiles() {
        var gap = of("tool_gap", "", "a Proxmox tool");
        assertEquals("concern", gap.signal());
        assertEquals("a Proxmox tool", gap.content());
        assertEquals("tool_gap", gap.metadata().get(SignalVerdicts.FIELD_KIND));
        var aside = of("stand_aside", "", "not my domain");
        assertEquals("stand_aside", aside.signal());
        assertEquals("not my domain", aside.content());
    }

    @Test
    void noDataIsAFailedGatherNeverAnAgree() {
        var v = of("no_data", "", "every query returned an empty result");
        assertEquals("failure", v.signal());
        assertEquals(SignalVerdicts.GATHER_FAILED, v.metadata().get(MetaKeys.FAILURE_TYPE));
        assertTrue(v.content().startsWith("No data: every query returned"));
        assertEquals("No data: the tools returned nothing usable", of("no_data", "", "").content());
    }

    @Test
    void aKindOutsideTheRuntimesTableIsPublishedUnderItsOwnName() {
        var terminal = SignalVerdicts.of(new SignalCall("vote_yes", "", "yes", "", ""), FINDING, true);
        assertEquals(SignalVerdicts.MSG_DECLARED, terminal.signal());
        assertEquals("vote_yes", terminal.metadata().get(SignalVerdicts.FIELD_KIND));
        assertEquals(true, terminal.metadata().get(SignalVerdicts.FIELD_TERMINAL));
        var open = SignalVerdicts.of(new SignalCall("note", "", "fyi", "", ""), FINDING, false);
        assertEquals(false, open.metadata().get(SignalVerdicts.FIELD_TERMINAL));
    }

    @Test
    void aFindingWithoutTextPostsTheStatementAndFiles() {
        var v = SignalVerdicts.of(new SignalCall("concern", "", "the count is wrong", "", ""),
                new SignalVerdicts.Finding("", FILES), true);
        assertEquals("the count is wrong\n\n" + FILES, v.content());
    }
}
