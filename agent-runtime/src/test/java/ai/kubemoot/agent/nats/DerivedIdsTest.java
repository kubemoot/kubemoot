package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.impl.Headers;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;

/** Ids made from event ids, and the publish that carries them. */
class DerivedIdsTest {

    private static final byte[] PAYLOAD = {1, 2, 3};

    @Test
    void aDerivedIdIsKeptAndAMissingOneBecomesARandomId() {
        assertEquals("t/0/closed/msg/thread_close", DerivedIds.idOrRandom("t/0/closed/msg/thread_close"));
        assertNotEquals(DerivedIds.idOrRandom(null), DerivedIds.idOrRandom(null));
    }

    @Test
    void theStreamIdNamesTheSubjectSoTwoSubjectsOfOneMessageAreBothStored() {
        assertNotEquals(DerivedIds.streamId("m", "kubemoot.discuss.ns.crew.broadcast.t"),
                DerivedIds.streamId("m", "kubemoot.discuss.ns.crew.kubernetes.t"));
        assertEquals(DerivedIds.streamId("m", "s"), DerivedIds.streamId("m", "s"), "the same decision, the same id");
    }

    @Test
    void aDerivedIdIsSentAsTheStreamsMessageIdHeader() {
        var conn = mock(Connection.class);

        DerivedIds.publish(conn, "kubemoot.discuss.ns.crew.broadcast.t", PAYLOAD, "m");

        var headers = ArgumentCaptor.forClass(Headers.class);
        verify(conn).publish(eq("kubemoot.discuss.ns.crew.broadcast.t"), headers.capture(), eq(PAYLOAD));
        assertEquals("m@kubemoot.discuss.ns.crew.broadcast.t", headers.getValue().getFirst(DerivedIds.MSG_ID_HEADER));
    }

    @Test
    void withoutADerivedIdTheMessageIsPublishedAsItAlwaysWasWithNoHeaders() {
        var conn = mock(Connection.class);

        DerivedIds.publish(conn, "s", PAYLOAD, null);

        verify(conn).publish("s", PAYLOAD);
        verify(conn, never()).publish(any(String.class), any(Headers.class), any(byte[].class));
    }
}
