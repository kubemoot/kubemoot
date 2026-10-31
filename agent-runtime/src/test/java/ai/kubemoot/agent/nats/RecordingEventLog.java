package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.Consumer;

/**
 * A fake coordinator log. It behaves like the stream: it numbers each subject's messages,
 * refuses an append whose expected last sequence is not the subject's last (fenced), and
 * answers a repeated event id with the original sequence. It sends each event through its
 * wire form (JSON bytes and back), so a test sees what a reader of the stream would see.
 * A test can make appends fail, or write to a subject as another writer would.
 */
final class RecordingEventLog implements CoordinatorEventLog {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final List<CoordinatorEvent> events = Collections.synchronizedList(new ArrayList<>());
    private final List<String> subjects = Collections.synchronizedList(new ArrayList<>());
    private final List<Long> expected = Collections.synchronizedList(new ArrayList<>());
    private final Map<String, Long> lastBySubject = new HashMap<>();
    private final Map<String, Long> sequenceById = new HashMap<>();
    private final Set<String> failTypes = new HashSet<>();
    private final List<String> refused = Collections.synchronizedList(new ArrayList<>());
    private final Map<String, Runnable> afterAppend = new HashMap<>();
    private Consumer<String> observer = type -> { };
    private final SimpleMeterRegistry registry = new SimpleMeterRegistry();
    private final DiscussionMetrics metrics = new DiscussionMetrics(registry);

    @Override
    public synchronized AppendResult append(String subject, CoordinatorEvent event, long expectedLastSequence) {
        if (failTypes.contains(event.type())) {
            refused.add(event.type());
            return AppendResult.failed();
        }
        Long original = sequenceById.get(event.eventId());
        if (original != null) {
            return AppendResult.appended(original);
        }
        long last = lastBySubject.getOrDefault(subject, 0L);
        if (expectedLastSequence != last) {
            return AppendResult.fenced();
        }
        try {
            var onTheWire = CoordinatorEvent.parse(MAPPER, MAPPER.writeValueAsBytes(event.toJson(MAPPER)));
            events.add(onTheWire);
            subjects.add(subject);
            expected.add(expectedLastSequence);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
        long sequence = last + 1;
        lastBySubject.put(subject, sequence);
        sequenceById.put(event.eventId(), sequence);
        observer.accept("event:" + event.type());
        Runnable hook = afterAppend.get(event.type());
        if (hook != null) {
            hook.run();
        }
        return AppendResult.appended(sequence);
    }

    /** Appends of events of {@code type} fail from now on, as if the stream refused them. */
    synchronized void failEventsOfType(String type) {
        failTypes.add(type);
    }

    /** Calls {@code observer} with {@code event:<type>} for each event stored, in order. */
    synchronized void observeWith(Consumer<String> observer) {
        this.observer = observer;
    }

    /** Runs {@code action} right after an event of {@code type} is stored (for example another writer appending). */
    synchronized void afterAppendOf(String type, Runnable action) {
        afterAppend.put(type, action);
    }

    /** Appends of events of {@code type} succeed again. */
    synchronized void stopFailing(String type) {
        failTypes.remove(type);
    }

    /** The types of the appends refused by {@link #failEventsOfType}, in order. */
    List<String> refusedTypes() {
        synchronized (refused) {
            return List.copyOf(refused);
        }
    }

    /** Another writer appends to {@code subject}: the next append by this writer is fenced. */
    synchronized void anotherWriterAppendsTo(String subject) {
        lastBySubject.merge(subject, 1L, Long::sum);
    }

    /** The sequence of the last message on {@code subject}. */
    synchronized long lastSequence(String subject) {
        return lastBySubject.getOrDefault(subject, 0L);
    }

    /** A copy of the events written so far, in order. */
    List<CoordinatorEvent> events() {
        synchronized (events) {
            return List.copyOf(events);
        }
    }

    List<String> subjects() {
        synchronized (subjects) {
            return List.copyOf(subjects);
        }
    }

    /** The expected last sequence each stored append stated, in order. */
    List<Long> expectedSequences() {
        synchronized (expected) {
            return List.copyOf(expected);
        }
    }

    /** The types of the events written so far, in order. */
    List<String> types() {
        return events().stream().map(CoordinatorEvent::type).toList();
    }

    /** The count of the untagged counter {@code name}. */
    double counted(String name) {
        return registry.get(name).counter().count();
    }

    /** The count of the {@code result} series of counter {@code name}. */
    double counted(String name, String result) {
        return registry.get(name).tag("result", result).counter().count();
    }

    /** The metrics the recorder of this log counts into. */
    DiscussionMetrics metrics() {
        return metrics;
    }

    /** A recorder that writes to this log. */
    CoordinatorEventRecorder recorder() {
        return new CoordinatorEventRecorder(true, this,
                (crew, threadId) -> "kubemoot.coordinator.ns." + crew + "." + threadId,
                "coordinator", MAPPER, java.time.Instant::now, metrics);
    }
}
