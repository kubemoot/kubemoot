package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The decision to convene analysts asks the same question the synthesis fill asks: do the results fit one round. */
class AnalystFirstDeliveryPlanTest {

    private static final String DIR = "ns/crew/t1/k8s/";
    private static final String RESUMES = "[{\"name\":\"compute\",\"role\":\"analyst\",\"tools\":[\"workspace_query\"]}]";

    private final ChatService chat = mock(ChatService.class);

    private AnalystFirstDelivery helper(String resumes) {
        when(chat.estimateTokens(anyString())).thenAnswer(inv -> (inv.<String>getArgument(0).length() + 3) / 4L);
        return new AnalystFirstDelivery(mock(NatsConnectionProvider.class), chat, new ObjectMapper(), () -> "SYSTEM",
                () -> resumes);
    }

    private static String key(int n) {
        return DIR + String.format("%03d", n) + "-pods_list";
    }

    private static StoredDelivery delivery(int... sizes) {
        var entries = new ArrayList<ThreadCatalog.Entry>();
        var thread = new StringBuilder("Discussion thread t1\n");
        var contents = new java.util.HashMap<String, String>();
        for (int i = 0; i < sizes.length; i++) {
            contents.put(key(i + 1), "x".repeat(sizes[i]));
            entries.add(new ThreadCatalog.Entry(key(i + 1), "k8s", "pods_list", "{}", -1, sizes[i], "text", false));
            thread.append("[CALL key=").append(key(i + 1)).append(" tool=pods_list format=text bytes=").append(sizes[i])
                    .append(" rows=unknown error=false]\n");
        }
        var catalog = new ThreadCatalog("ns/crew/t1/", entries);
        var coverage = EvidenceCoverage.compute(catalog, Map.of(), List.of());
        return StoredDelivery.prepare("", thread.toString(), catalog, coverage, contents::get);
    }

    private void room(long tokens) {
        when(chat.simpleCallRoomTokens(any(), any())).thenReturn(OptionalLong.of(tokens));
    }

    @Test
    void resultsThatTogetherFitTheRoomAreNotConvened() {
        room(1_000);

        assertFalse(helper(RESUMES).plan(delivery(1_000, 1_000, 1_000), 0).convenes());
    }

    @Test
    void oneResultTooBigForTheRoomConvenesTheAnalystEvenWhenTheOthersAreSmall() {
        room(1_000);

        var plan = helper(RESUMES).plan(delivery(100, 8_000), 0);

        assertTrue(plan.convenes());
        assertEquals("compute", plan.assignments().get(0).analyst());
    }

    @Test
    void resultsThatFitIndividuallyButNotTogetherAreConvened() {
        room(1_000);

        assertTrue(helper(RESUMES).plan(delivery(2_000, 2_000, 2_000), 0).convenes());
    }

    @Test
    void anUnknownWindowIsOneRoundAndNotConvened() {
        when(chat.simpleCallRoomTokens(any(), any())).thenReturn(OptionalLong.empty());

        assertFalse(helper(RESUMES).plan(delivery(900_000), 0).convenes());
    }

    @Test
    void aCrewWithNoQueryAnalystIsNotConvenedAndAnAlreadyConvenedThreadIsNot() {
        room(10);

        assertFalse(helper("[]").plan(delivery(8_000), 0).convenes());
        assertFalse(helper(RESUMES).plan(delivery(8_000), DeliveryPlan.MAX_CONVENINGS).convenes());
        assertFalse(helper(null).plan(delivery(8_000), 0).convenes());
    }

    @Test
    void aThreadWithNoCatalogIsNeverConvened() {
        var empty = new ThreadCatalog("", List.of());
        var delivery = StoredDelivery.prepare("", "Discussion thread t1\n", empty,
                EvidenceCoverage.compute(empty, Map.of(), List.of()), k -> null);
        room(0);

        assertFalse(helper(RESUMES).plan(delivery, 0).convenes());
    }
}
