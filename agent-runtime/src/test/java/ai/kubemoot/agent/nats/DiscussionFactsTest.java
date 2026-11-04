package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import ai.kubemoot.agent.nats.DiscussionFacts.Message;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** When tools ran and no agent stated a fact or posted a result, the coordinator answers without the model. */
class DiscussionFactsTest {

    private static final String HEADER = "[WORKSPACE agent=k8s-workloads calls=3 errors=0 prefix=k8s-workloads/]\n";
    private static final String CALL1 = "[CALL key=k8s-workloads/001-resources_list tool=resources_list "
            + "args={\"kind\": \"CronJob\"} format=text-table bytes=856 rows=unknown error=false columns=NAMESPACE,NAME]";
    private static final String CALL2 = "[CALL key=k8s-workloads/002-resources_list tool=resources_list "
            + "args={\"kind\": \"Job\"} format=text-table bytes=25662 rows=unknown error=false columns=NAMESPACE,NAME]";
    private static final String CALL3 = "[CALL key=k8s-workloads/003-events_list tool=events_list args={} "
            + "format=text bytes=17 rows=unknown error=false]";
    private static final String FOOTER = "\nThe results are stored under these keys; a result not shown above is only in its "
            + "file. Read a key with workspace_query.";
    private static final String DESCRIPTIONS = HEADER + CALL1 + "\n" + CALL2 + "\n" + CALL3 + FOOTER;
    private static final String QUESTION = "List the CronJobs and recent Jobs in the cluster.";

    private static Message agree(String agent, String content) {
        return new Message(agent, "agree", content);
    }

    @Test
    void descriptionsOnlyHoldNoFacts() {
        var thread = List.of(agree("k8s-workloads", DESCRIPTIONS));

        assertTrue(DiscussionFacts.toolsRan(thread));
        assertFalse(DiscussionFacts.holdsFacts(thread));
        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void aPostedResultIsAFactEvenWhenTheRestIsDescriptions() {
        String withResult = HEADER + CALL1 + "\n[RESULT]\nNAMESPACE NAME\ndefault backup\n[END RESULT]\n" + CALL3 + FOOTER;
        var thread = List.of(agree("k8s-workloads", withResult));

        assertTrue(DiscussionFacts.holdsFacts(thread));
        assertFalse(DiscussionFacts.noData(thread));
    }

    @Test
    void aBlankResultBlockIsNotAFact() {
        var thread = List.of(agree("a", HEADER + CALL1 + "\n[RESULT]\n \n[END RESULT]" + FOOTER));

        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void theErrorTextOfAFailedCallIsNotAFact() {
        String failed = HEADER + CALL1.replace("error=false", "error=true")
                + "\n[FAILED RESULT]\nobject not found\n[END RESULT]" + FOOTER;
        var thread = List.of(agree("compute", failed));

        assertFalse(DiscussionFacts.holdsFacts(thread));
        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void anAgentsOwnTextBesideDescriptionsIsAFact() {
        var thread = List.of(agree("compute", "There are 3 CronJobs.\n\n" + DESCRIPTIONS));

        assertTrue(DiscussionFacts.holdsFacts(thread));
        assertFalse(DiscussionFacts.noData(thread));
    }

    @Test
    void anotherAgentsFactMakesTheThreadHoldFactsWhateverTheToolerPosted() {
        var thread = List.of(agree("k8s-workloads", DESCRIPTIONS), agree("k8s-analyst", "3 CronJobs exist"));

        assertFalse(DiscussionFacts.noData(thread));
    }

    @Test
    void protocolWordsEmptyAgreesAndSignalsWithoutTextAreNotFacts() {
        var thread = List.of(
                agree("k8s-workloads", DESCRIPTIONS),
                agree("k8s-analyst", ""),
                agree("k8s-nodes", "   \n"),
                agree("k8s-metrics", "NOTHING_TO_ADD"),
                agree("compute", "NO_DATA"),
                new Message("a", "stand_aside", "I have nothing to add here"),
                new Message("b", "failure", "Tool 'workspace_query' failed 2 times in this evaluation"),
                new Message("c", "decline", "not my area"),
                new Message("homelab-coordinator", "advisory", "k8s-workloads: list the CronJobs"),
                new Message("homelab-coordinator", "review_ready", "[CATALOG calls=3] ... some listing"),
                new Message("human", "thread_start", QUESTION));

        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void aConcernCarriesTextSoTheThreadIsNotAnsweredWithoutTheModel() {
        var thread = List.of(agree("k8s-workloads", DESCRIPTIONS),
                new Message("k8s-analyst", "concern", "TOOL_GAP: need a tool that lists CronJobs"));

        assertFalse(DiscussionFacts.noData(thread), "gap handling needs the model's path");
    }

    @Test
    void aThreadWhereNoToolRanIsNotANoDataThread() {
        assertFalse(DiscussionFacts.noData(List.of()));
        assertFalse(DiscussionFacts.noData(List.of(agree("a", ""))));
        assertFalse(DiscussionFacts.noData(List.of(new Message("a", "stand_aside", ""))),
                "all stood aside: the model still answers a general question");
        assertFalse(DiscussionFacts.noData(List.of(new Message("a", "failure", "boom"))),
                "a failure with no stored call keeps the existing sources-not-reached answer");
    }

    @Test
    void aNotStoredLineCountsAsToolsThatRan() {
        var thread = List.of(agree("k8s-workloads", "[WORKSPACE agent=k8s-workloads calls=1 errors=0 prefix=]\n"
                + "[NOT STORED tool=pods_list args={} error=false - the result could not be written to the workspace "
                + "and is not available]"));

        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void theCountLineOfALongManifestIsNotAFact() {
        var thread = List.of(agree("a", HEADER + CALL1 + CallManifest.omittedLine(14) + FOOTER));

        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void nullContentIsToleratedAsEmpty() {
        var thread = List.of(new Message("a", "agree", null), agree("k8s-workloads", DESCRIPTIONS));

        assertTrue(DiscussionFacts.noData(thread));
    }

    @Test
    void theAnswerNamesTheQuestionTheToolsThatRanAndThatNoDataWasObtained() {
        var thread = List.of(agree("k8s-workloads", DESCRIPTIONS));

        String answer = DiscussionFacts.noDataAnswer(QUESTION, thread, Map.of());

        assertTrue(answer.startsWith("No data was obtained to answer this question"), answer);
        assertTrue(answer.contains("Question: " + QUESTION), answer);
        assertTrue(answer.contains("- k8s-workloads: resources_list (2 calls), events_list (1 call)"), answer);
        assertFalse(answer.contains("Failures:"), answer);
        assertFalse(answer.contains("[CALL"), "no internal line reaches the user: " + answer);
        assertFalse(answer.contains("k8s-workloads/001"), "no key reaches the user: " + answer);
        assertTrue(answer.contains("nothing can be reported as fact"), answer);
    }

    @Test
    void theAnswerLeadsTheFailuresWithTheirTextFromTheCallAndFromTheSignals() {
        String failed = HEADER + CALL1 + "\n" + CALL2.replace("error=false", "error=true")
                + "\n[FAILED RESULT]\nforbidden:\n  cronjobs.batch is forbidden\n[END RESULT]\n"
                + "[NOT STORED tool=nodes_list args={} error=true - the result could not be written to the workspace "
                + "and is not available]" + FOOTER;
        var thread = List.of(agree("k8s-workloads", failed), agree("compute", HEADER.replace("k8s-workloads", "compute")
                + "[CALL key=compute/001-workspace_query tool=workspace_query args={} format=text bytes=3 rows=unknown "
                + "error=true]" + FOOTER));

        String answer = DiscussionFacts.noDataAnswer(QUESTION, thread,
                Map.of("compute", "Tool 'workspace_query' failed 2 times in this evaluation"));

        assertTrue(answer.contains("Failures:"), answer);
        assertTrue(answer.contains("- k8s-workloads, resources_list: forbidden: cronjobs.batch is forbidden"), answer);
        assertTrue(answer.contains("- compute, workspace_query: the tool returned an error"),
                "a failed call whose text was not posted still shows: " + answer);
        assertTrue(answer.contains("- compute: Tool 'workspace_query' failed 2 times in this evaluation"), answer);
        assertTrue(answer.contains("resources_list (2 calls, 1 failed)"), answer);
        assertTrue(answer.contains("nodes_list (1 call, 1 failed)"), answer);
    }

    @Test
    void aLongFailureTextAndQuestionAreCutAndKeptOnOneLine() {
        String longText = "boom ".repeat(200);
        String failed = HEADER + CALL1.replace("error=false", "error=true") + "\n[FAILED RESULT]\n" + longText
                + "\n[END RESULT]" + FOOTER;

        String answer = DiscussionFacts.noDataAnswer("q ".repeat(600), List.of(agree("a", failed)), Map.of());

        assertTrue(answer.contains("boom boom"), answer);
        assertTrue(answer.contains("...\n"), "the failure text is cut: " + answer);
        assertTrue(answer.length() < 1_500, "length " + answer.length());
        assertEquals(1, answer.lines().filter(l -> l.startsWith("Question: ")).count());
    }

    @Test
    void aMissingQuestionIsSaidSo() {
        String answer = DiscussionFacts.noDataAnswer(null, List.of(agree("a", DESCRIPTIONS)), Map.of());

        assertTrue(answer.contains("Question: (not recorded)"), answer);
    }

    @Test
    void theAnswerPassesThePublishedFormUnchanged() {
        String answer = DiscussionFacts.noDataAnswer(QUESTION, List.of(agree("k8s-workloads", DESCRIPTIONS)), Map.of());

        assertEquals(answer, DiscussionOrchestrator.publishedContent("synthesis", answer),
                "the user-facing clean-up leaves it alone");
    }

    @Test
    void theDescriptionsAndThePostedResultsOfOneContributionStayWithinWhatAnAgentMayPost() {
        assertTrue(CallManifest.MAX_CHARS + CallManifest.MAX_INLINE_CHARS <= DiscussionSubscriber.STATEMENT_MAX_CHARS,
                "a tooler's manifest is its statement, which the bus cuts at that size");
    }
}
