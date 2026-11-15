package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import ai.kubemoot.agent.nats.DiscussionFacts.Message;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** When tools ran and no agent stated a fact or posted a result, the coordinator answers without the model. */
class DiscussionFactsTest {

    private static final String HEADER = "[WORKSPACE agent=k8s-workloads calls=3 errors=0 prefix=k8s-workloads/]\n";
    private static final String CALL1 = "[CALL key=k8s-workloads/001-resources_list tool=resources_list "
            + "args={\"kind\": \"CronJob\"} type=text/plain; profile=kubectl-table bytes=856 rows=unknown error=false columns=NAMESPACE,NAME]";
    private static final String CALL2 = "[CALL key=k8s-workloads/002-resources_list tool=resources_list "
            + "args={\"kind\": \"Job\"} type=text/plain; profile=kubectl-table bytes=25662 rows=unknown error=false columns=NAMESPACE,NAME]";
    private static final String CALL3 = "[CALL key=k8s-workloads/003-events_list tool=events_list args={} "
            + "type=text/plain bytes=17 rows=unknown error=false]";
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
    void theAnswerNamesTheQuestionAndNoAgentToolKeyOrCount() {
        String answer = DiscussionFacts.noDataAnswer(QUESTION, false);

        assertTrue(answer.startsWith("I could not get the data needed to answer this question"), answer);
        assertTrue(answer.contains("You asked: " + QUESTION), answer);
        assertTrue(answer.contains("Every lookup this needed failed"), answer);
        for (String internal : List.of("k8s-workloads", "resources_list", "events_list", "workspace_query",
                "[CALL", "001", "failed 2 times", "(1 call", "agent", "tool ")) {
            assertFalse(answer.contains(internal), internal + " reached the user: " + answer);
        }
    }

    @Test
    void anAnswerForStoredResultsThatCouldNotBeReadSaysSoWithoutInternals() {
        String answer = DiscussionFacts.noDataAnswer(QUESTION, true);

        assertTrue(answer.contains("Some lookups returned results, but none of them could be read here"), answer);
        assertFalse(answer.contains("Every lookup"), answer);
        assertFalse(answer.contains("too large to post"), "a mechanism, not the asker's terms: " + answer);
    }

    @Test
    void aFailedLookupIsNeverReportedAsZeroItems() {
        assertTrue(DiscussionFacts.noDataAnswer(QUESTION, false).contains("no item is reported as missing or as zero"));
    }

    @Test
    void aLongQuestionIsCutAndKeptOnOneLine() {
        String answer = DiscussionFacts.noDataAnswer("q ".repeat(600), false);

        assertTrue(answer.length() < 1_000, "length " + answer.length());
        assertEquals(1, answer.lines().filter(l -> l.startsWith("You asked: ")).count());
        assertTrue(answer.contains("...\n"), answer);
    }

    @Test
    void aMissingQuestionIsSaidSo() {
        assertTrue(DiscussionFacts.noDataAnswer(null, false).contains("You asked: (not recorded)"));
    }

    @Test
    void theSuccessfulStoredCallsAreFoundOnceEachAndFailuresAreLeftOut() {
        String failed = HEADER + CALL1.replace("error=false", "error=true") + "\n" + CALL2 + "\n"
                + "[NOT STORED tool=nodes_list args={} error=false - the result could not be written]" + FOOTER;
        var thread = List.of(agree("k8s-workloads", failed), agree("k8s-workloads", failed));

        var found = DiscussionFacts.storedSuccesses(thread);

        assertEquals(List.of(new DiscussionFacts.StoredCall("k8s-workloads", "resources_list",
                "k8s-workloads/002-resources_list")), found);
    }

    @Test
    void aThreadWithOnlyFailuresHasNoStoredSuccess() {
        var thread = List.of(agree("a", HEADER + CALL1.replace("error=false", "error=true") + FOOTER));

        assertTrue(DiscussionFacts.storedSuccesses(thread).isEmpty());
    }

    @Test
    void theAnswerPassesThePublishedFormUnchanged() {
        String answer = DiscussionFacts.noDataAnswer(QUESTION, false);

        assertEquals(answer, DiscussionOrchestrator.publishedContent("synthesis", answer),
                "the user-facing clean-up leaves it alone");
    }

    @Test
    void theDescriptionsAndThePostedResultsOfOneContributionStayWithinWhatAnAgentMayPost() {
        assertTrue(CallManifest.MAX_CHARS + CallManifest.MAX_INLINE_CHARS <= DiscussionSubscriber.STATEMENT_MAX_CHARS,
                "a tooler's manifest is its statement, which the bus cuts at that size");
    }
}
