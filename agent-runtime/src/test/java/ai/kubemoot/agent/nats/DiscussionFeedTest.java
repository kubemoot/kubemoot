package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.JetStream;
import io.nats.client.Message;
import io.nats.client.MessageHandler;
import io.nats.client.PushSubscribeOptions;
import io.nats.client.impl.NatsJetStreamMetaData;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The live feed of a coordinator with leases: messages with their stream sequences, or a plain subscription when it cannot be had. */
class DiscussionFeedTest {

    private final Connection conn = mock(Connection.class);
    private final Dispatcher dispatcher = mock(Dispatcher.class);
    private final JetStream js = mock(JetStream.class);
    private final List<String> bodies = new ArrayList<>();
    private final List<Long> sequences = new ArrayList<>();

    private Message message(String body, long sequence) {
        var msg = mock(Message.class);
        when(msg.getData()).thenReturn(body.getBytes(StandardCharsets.UTF_8));
        var meta = mock(NatsJetStreamMetaData.class);
        when(meta.streamSequence()).thenReturn(sequence);
        when(msg.metaData()).thenReturn(meta);
        return msg;
    }

    private MessageHandler followWithAConsumer(DiscussionFeed.Handler handler) throws Exception {
        when(conn.jetStream()).thenReturn(js);
        DiscussionFeed.subscribe(conn, dispatcher, "kubemoot.discuss.ns.crew.>", handler);
        ArgumentCaptor<MessageHandler> captured = ArgumentCaptor.forClass(MessageHandler.class);
        verify(js).subscribe(eq("kubemoot.discuss.ns.crew.>"), eq(dispatcher), captured.capture(), eq(false),
                any(PushSubscribeOptions.class));
        return captured.getValue();
    }

    @Test
    void eachMessageIsHandedOverWithItsStreamSequence() throws Exception {
        MessageHandler feed = followWithAConsumer((data, sequence) -> {
            bodies.add(data);
            sequences.add(sequence);
        });

        feed.onMessage(message("{\"a\":1}", 41));
        feed.onMessage(message("{\"a\":2}", 42));

        assertEquals(List.of("{\"a\":1}", "{\"a\":2}"), bodies);
        assertEquals(List.of(41L, 42L), sequences);
    }

    @Test
    void aMessageWhoseSequenceCannotBeReadHasSequenceZero() throws Exception {
        MessageHandler feed = followWithAConsumer((data, sequence) -> sequences.add(sequence));
        var msg = mock(Message.class);
        when(msg.getData()).thenReturn("{}".getBytes(StandardCharsets.UTF_8));
        when(msg.metaData()).thenThrow(new IllegalStateException("not a stream message"));

        feed.onMessage(msg);

        assertEquals(List.of(0L), sequences);
    }

    @Test
    void aHandlerThatFailsDoesNotStopTheFeed() throws Exception {
        MessageHandler feed = followWithAConsumer((data, sequence) -> {
            bodies.add(data);
            throw new IllegalArgumentException("bad message");
        });

        assertDoesNotThrow(() -> feed.onMessage(message("one", 1)));
        assertDoesNotThrow(() -> feed.onMessage(message("two", 2)));

        assertEquals(List.of("one", "two"), bodies, "the next message is still handed over");
    }

    @Test
    void whenTheStreamCannotBeFollowedTheFeedFallsBackToAPlainSubscriptionWithNoSequences() throws Exception {
        when(conn.jetStream()).thenThrow(new IOException("no JetStream"));

        DiscussionFeed.subscribe(conn, dispatcher, "kubemoot.discuss.ns.crew.>", (data, sequence) -> {
            bodies.add(data);
            sequences.add(sequence);
        });

        ArgumentCaptor<MessageHandler> plain = ArgumentCaptor.forClass(MessageHandler.class);
        verify(dispatcher).subscribe(eq("kubemoot.discuss.ns.crew.>"), plain.capture());
        var msg = mock(Message.class);
        when(msg.getData()).thenReturn("hello".getBytes(StandardCharsets.UTF_8));
        plain.getValue().onMessage(msg);
        assertEquals(List.of("hello"), bodies);
        assertEquals(List.of(0L), sequences);
        verify(js, org.mockito.Mockito.never()).subscribe(anyString(), any(Dispatcher.class), any(MessageHandler.class),
                anyBoolean(), any(PushSubscribeOptions.class));
    }
}
