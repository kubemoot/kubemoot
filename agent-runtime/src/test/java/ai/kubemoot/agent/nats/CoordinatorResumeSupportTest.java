package ai.kubemoot.agent.nats;

import io.nats.client.Message;
import io.nats.client.impl.NatsJetStreamMetaData;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The small pieces resume leans on: the thread's subject filter, the open-once rule, the takeover event, the request's sequence. */
class CoordinatorResumeSupportTest extends CoordinatorThreadTestBase {

    @Test
    void oneThreadOnEveryChannelHasOneSubjectFilter() {
        assertEquals("kubemoot.discuss.ns.crew.*.t1", CrewScope.of("ns", "crew").threadWildcard("t1"));
        assertEquals("kubemoot.discuss.ns.*.t1", CrewScope.of("ns", null).threadWildcard("t1"), "crewless: no crew token");
        assertTrue(FakeBusReader.matches(CrewScope.of("ns", "crew").threadWildcard("t1"),
                "kubemoot.discuss.ns.crew.broadcast.t1"));
        assertFalse(FakeBusReader.matches(CrewScope.of("ns", "crew").threadWildcard("t1"),
                "kubemoot.discuss.ns.crew.broadcast.t2"));
    }

    @Test
    void aThreadIsOpenedOnceEvenWhenTheOpeningIsAskedForAgain() throws Exception {
        String t = "t-once";
        send(t, "human", "thread_start", "q", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        int before = forThread(t).size();

        assertTrue(orchestrator.recorder().opened(orchestrator.threadForTest(t), "external"));

        assertEquals(before, forThread(t).size(), "no second thread_opened");
    }

    @Test
    void aTakeoverRefusedBecauseAnotherCoordinatorWroteLastFencesTheThread() throws Exception {
        String t = "t-fence";
        send(t, "human", "thread_start", "q", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        eventLog.anotherWriterAppendsTo(eventLog.subjects().get(0));

        boolean allowed = orchestrator.recorder().resumed(orchestrator.threadForTest(t), 2);

        assertFalse(allowed, "the takeover is refused");
        assertTrue(orchestrator.recorder().fenced(orchestrator.threadForTest(t)));
    }

    @Test
    void theTakeoverIsLoggedOnceWithTheSequenceItStartedFrom() throws Exception {
        String t = "t-takeover";
        send(t, "human", "thread_start", "q", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        var state = orchestrator.threadForTest(t);

        assertTrue(orchestrator.recorder().resumed(state, 2));
        assertTrue(orchestrator.recorder().resumed(state, 2), "the same takeover is an event of its own number, not a repeat");

        var resumes = forThread(t).stream().filter(e -> e.type().equals("coordinator_resumed")).toList();
        assertEquals(2, resumes.size());
        assertEquals(t + "/0/resumed/1", resumes.get(0).eventId());
        assertEquals(2, resumes.get(0).data().path("fromSeq").asLong());
        assertEquals(state.threadCreated.toString(), resumes.get(0).data().path("resumedAt").asText());
    }

    @Test
    void theStreamSequenceOfARequestIsReadFromItsMetadata() {
        var msg = mock(Message.class);
        var meta = mock(NatsJetStreamMetaData.class);
        when(msg.metaData()).thenReturn(meta);
        when(meta.streamSequence()).thenReturn(17L);

        assertEquals(17L, RequestQueueConsumer.streamSequence(msg));
    }

    @Test
    void aRequestWhoseMetadataCannotBeReadHasSequenceZero() {
        var msg = mock(Message.class);
        when(msg.metaData()).thenThrow(new IllegalStateException("not a JetStream message"));

        assertEquals(0L, RequestQueueConsumer.streamSequence(msg));
        assertEquals(0L, RequestQueueConsumer.streamSequence(mock(Message.class)), "no metadata at all");
    }
}
