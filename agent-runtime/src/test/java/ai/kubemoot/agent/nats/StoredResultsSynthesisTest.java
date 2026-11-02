package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.PromptWindow;
import ai.kubemoot.agent.chat.SimplePrompt;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;
import java.util.function.Function;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * A whole thread whose tooler posted a manifest of stored results: the coordinator reads each stored
 * result once, sends it to the synthesis model in prompts sized to the window of that round, and the
 * synthesis event carries the rounds and the bytes. A thread without a manifest synthesizes as it did.
 */
class StoredResultsSynthesisTest extends CoordinatorThreadTestBase {

    private static final String PREFIX = "ns/crew/t-store/k8s-workloads/";

    @Override
    protected boolean withBus() {
        return true;
    }

    private final List<SimplePrompt> prompts = Collections.synchronizedList(new ArrayList<>());
    private final List<Long> reads = Collections.synchronizedList(new ArrayList<>());

    private static String key(int i) {
        return PREFIX + "00" + i + "-tool" + i;
    }

    private static String stored(int i) {
        return "UNIQUE-" + i + "\n" + (("row " + i + " ").repeat(20).substring(0, 99) + "\n").repeat(200);
    }

    private static String manifest(int calls) {
        var sb = new StringBuilder("[WORKSPACE agent=k8s-workloads calls=" + calls + " errors=0 prefix=" + PREFIX + "]\n");
        for (int i = 1; i <= calls; i++) {
            sb.append("[CALL key=").append(key(i)).append(" tool=tool").append(i)
                    .append(" format=text bytes=").append(stored(i).length()).append(" rows=unknown error=false]\n");
        }
        return sb.append("The results are stored under these keys and are not shown here. Read a key with workspace_query.")
                .toString();
    }

    /** The object store serves {@code calls} results, and every read is counted. */
    private void storeHolds(int calls) throws Exception {
        Connection conn = natsProvider.getConnection();
        ObjectStore os = mock(ObjectStore.class);
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenReturn(os);
        doAnswer(inv -> {
            String key = inv.getArgument(0);
            int i = Integer.parseInt(key.substring(key.indexOf("/00") + 3, key.indexOf("-tool")));
            reads.add((long) i);
            ((OutputStream) inv.getArgument(1)).write(stored(i).getBytes(StandardCharsets.UTF_8));
            return null;
        }).when(os).get(anyString(), any(OutputStream.class));
        when(chatService.estimateTokens(any())).thenAnswer(inv -> (inv.<String>getArgument(0).length() + 3) / 4L);
    }

    /** The synthesis model: every call gets a window of {@code window} tokens; notes rounds reply with notes. */
    private void modelWithWindow(long window) {
        when(chatService.simpleLlmCallFor(any(), any(), any())).thenAnswer(call -> {
            Function<PromptWindow, SimplePrompt> promptFor = call.getArgument(2);
            var prompt = promptFor.apply(new PromptWindow() {
                @Override
                public OptionalLong tokens() {
                    return OptionalLong.of(window);
                }

                @Override
                public OptionalLong roomAfter(String system, String user) {
                    long used = (system.length() + user.length() + 6) / 4L;
                    return OptionalLong.of(Math.max(0, window - used - window / 8));
                }
            });
            prompts.add(prompt);
            boolean notes = prompt.user().contains("STORED RESULTS (round");
            return new ChatService.SimpleLlmResult(notes ? "- [noted] findings" : "Eleven pods.", 100, 20);
        });
    }

    private void start(String t) throws Exception {
        send(t, "human", "thread_start", "How many pods run?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
    }

    private void runThreadWithManifest(String t, int calls) throws Exception {
        start(t);
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "agree", "11 pods", null);
        send(t, "k8s-workloads", "agree", manifest(calls), null);
        settle(t, EVALUATION_TO_REVIEW);
        send(t, "k8s-analyst", "agree", "checked", null);
        settle(t, REVIEW_TO_SYNTHESIS);
        awaitTypes(t, 1, "synthesis_done");
    }

    private Map<String, Object> synthesisDone(String t) {
        var data = forThread(t).stream().filter(e -> e.type().equals("synthesis_done")).findFirst().orElseThrow().data();
        return Map.of("rounds", data.path("rounds").asInt(), "filledBytes", data.path("filledBytes").asLong(),
                "response", data.path("response").asText());
    }

    @Test
    void aSmallWindowReadsEveryStoredResultOnceInSeveralRoundsAndTheEventSaysSo() throws Exception {
        storeHolds(4);
        modelWithWindow(8_192);

        runThreadWithManifest("t-store", 4);

        var done = synthesisDone("t-store");
        int rounds = (int) done.get("rounds");
        assertTrue(rounds > 1, "rounds " + rounds);
        assertEquals(rounds, prompts.size());
        long total = 0;
        for (int i = 1; i <= 4; i++) {
            total += stored(i).getBytes(StandardCharsets.UTF_8).length;
            int sent = 0;
            for (SimplePrompt p : prompts) {
                sent += p.user().split("UNIQUE-" + i + "\n", -1).length - 1;
            }
            assertEquals(1, sent, "result " + i + " reached the model once");
        }
        assertEquals(total, done.get("filledBytes"));
        assertEquals("Eleven pods.", done.get("response"));
        assertEquals(List.of(1L, 2L, 3L, 4L), reads.stream().sorted().toList(), "each stored result is read from the store once");
        assertTrue(prompts.get(prompts.size() - 1).user().contains("WORKING NOTES FROM THE STORED TOOL RESULTS"));
    }

    @Test
    void aLargeWindowIsOneRoundWithTheResultsInPlace() throws Exception {
        storeHolds(2);
        modelWithWindow(131_072);

        runThreadWithManifest("t-store", 2);

        assertEquals(1, prompts.size());
        assertEquals(1, synthesisDone("t-store").get("rounds"));
        assertTrue(prompts.get(0).user().contains("[tool1]\nUNIQUE-1") && prompts.get(0).user().contains("[tool2]\nUNIQUE-2"));
        assertFalse(prompts.get(0).user().contains("[CALL key="), "the call lines are replaced by the results");
        assertFalse(prompts.get(0).user().contains("[WORKSPACE"), "the manifest header is gone");
    }

    @Test
    void aThreadWithoutAManifestSynthesizesAsBeforeWithOneRoundAndNoStoredBytes() throws Exception {
        modelWithWindow(8_192);

        runFullThreadWithoutManifest();

        var done = synthesisDone("t-plain");
        assertEquals(1, done.get("rounds"));
        assertEquals(0L, done.get("filledBytes"));
        assertEquals("Eleven pods.", done.get("response"));
        verify(chatService, never()).simpleLlmCallFor(any(), any(), any());
        verify(chatService, atLeastOnce()).simpleLlmCallWithTokens(any(), any());
    }

    private void runFullThreadWithoutManifest() throws Exception {
        start("t-plain");
        advanceThroughAdvisory("t-plain");
        send("t-plain", "k8s-metrics", "agree", "11 pods", null);
        send("t-plain", "k8s-workloads", "agree", "12 pods", null);
        settle("t-plain", EVALUATION_TO_REVIEW);
        send("t-plain", "k8s-analyst", "agree", "checked", null);
        settle("t-plain", REVIEW_TO_SYNTHESIS);
        awaitTypes("t-plain", 1, "synthesis_done");
    }

    @Test
    void theRoundCapIsASettingAndDefaultsToFour() throws Exception {
        storeHolds(4);
        modelWithWindow(2_048);

        runThreadWithManifest("t-store", 4);

        assertEquals(4, synthesisDone("t-store").get("rounds"), "the default cap of four rounds");
        verify(chatService, times(4)).simpleLlmCallFor(any(), any(), any());
        String last = prompts.get(3).user();
        assertTrue(last.contains("could NOT be retrieved") && last.contains("Treat it as UNKNOWN"),
                "what the cap left unread is named unknown: " + last);
    }
}
