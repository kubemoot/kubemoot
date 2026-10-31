package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * A fake publisher for coordinator events. It sends each event through its wire form
 * (JSON bytes and back), so a test sees what a reader of the stream would see, and keeps
 * the subjects the events were written to.
 */
final class RecordingEventLog implements CoordinatorEventLog {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final List<CoordinatorEvent> events = Collections.synchronizedList(new ArrayList<>());
    private final List<String> subjects = Collections.synchronizedList(new ArrayList<>());

    @Override
    public void append(String subject, CoordinatorEvent event) {
        try {
            var onTheWire = CoordinatorEvent.parse(MAPPER, MAPPER.writeValueAsBytes(event.toJson(MAPPER)));
            events.add(onTheWire);
            subjects.add(subject);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
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

    /** The types of the events written so far, in order. */
    List<String> types() {
        return events().stream().map(CoordinatorEvent::type).toList();
    }

    /** A recorder that writes to this log. */
    CoordinatorEventRecorder recorder() {
        return new CoordinatorEventRecorder(true, this,
                (crew, threadId) -> "kubemoot.coordinator.ns." + crew + "." + threadId,
                "coordinator", MAPPER, java.time.Instant::now);
    }
}
