package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.function.ToLongFunction;

/**
 * The stored results still to be read into a synthesis, in manifest order. A round takes from the
 * front of the queue what fits its room: {@link #fill} takes whole results (one that does not fit
 * is passed over and the ones after it are still considered), and {@link #batch} also splits a
 * result that cannot fit even an empty round into consecutive parts at line boundaries. Planning
 * changes nothing; {@link #commit} does, once the round's call has succeeded, so a failed call
 * leaves every result unread and the next round plans again. Every result is read exactly once.
 */
final class EvidenceQueue {

    /** The part of a result a round delivers; {@code part} is 0 when it is the whole result. */
    record Slice(String key, String tool, String text, int part, int parts) {

        boolean isPart() {
            return part > 0;
        }

        /** The slice as a prompt shows it: {@code [tool]}, or with its key (and part) in a notes round. */
        String render(boolean withKey) {
            return label(withKey) + "\n" + text;
        }

        String label(boolean withKey) {
            var sb = new StringBuilder("[").append(tool);
            if (withKey) {
                sb.append(" key=").append(key);
            }
            if (isPart()) {
                sb.append(" part ").append(part).append(" of ").append(parts);
            }
            return sb.append(']').toString();
        }
    }

    /** A planned round: the slices it takes, and what the queue holds once they are read. */
    static final class Batch {
        private final List<Taken> taken = new ArrayList<>();

        List<Slice> slices() {
            return taken.stream().map(Taken::slice).toList();
        }

        boolean isEmpty() {
            return taken.isEmpty();
        }

        int size() {
            return taken.size();
        }
    }

    private record Taken(Item item, Slice slice, int newOffset) {}

    private static final class Item {
        final StoredResult result;
        int offset;
        int partsRead;

        Item(StoredResult result) {
            this.result = result;
        }

        String remaining() {
            return result.content().substring(offset);
        }
    }

    private final List<Item> pending = new ArrayList<>();
    private final ToLongFunction<String> tokens;

    /** A queue of the readable results among {@code results}. */
    EvidenceQueue(List<StoredResult> results, ToLongFunction<String> tokens) {
        this.tokens = tokens;
        results.stream().filter(StoredResult::readable).forEach(r -> pending.add(new Item(r)));
    }

    int size() {
        return pending.size();
    }

    boolean isEmpty() {
        return pending.isEmpty();
    }

    /** True when some of the result was read in an earlier round and the rest is still pending. */
    boolean partlyRead(String key) {
        return pending.stream().anyMatch(i -> i.result.key().equals(key) && i.partsRead > 0);
    }

    /** True when the result is not in the queue any more: all of it was read. */
    boolean isRead(String key) {
        return pending.stream().noneMatch(i -> i.result.key().equals(key));
    }

    /** The pending results that fit {@code room} whole, in order; one that does not fit does not stop the rest. */
    Batch fill(long room) {
        var batch = new Batch();
        long left = room;
        for (Item item : pending) {
            Slice whole = whole(item);
            long cost = tokens.applyAsLong(whole.render(false));
            if (cost <= left) {
                batch.taken.add(new Taken(item, whole, item.result.content().length()));
                left -= cost;
            }
        }
        return batch;
    }

    /** Every pending result, each cut at {@code maxChars} characters: the fill when no window is known. */
    Batch cut(int maxChars) {
        var batch = new Batch();
        for (Item item : pending) {
            Slice whole = whole(item);
            var cut = new Slice(whole.key(), whole.tool(), ManifestInliner.cut(whole.text(), maxChars), whole.part(),
                    whole.parts());
            batch.taken.add(new Taken(item, cut, item.result.content().length()));
        }
        return batch;
    }

    /**
     * What a notes round of {@code room} tokens takes: results whole while they fit, and, for a
     * result that cannot fit an empty round, its next part when it is first in the round.
     */
    Batch batch(long room) {
        var batch = new Batch();
        long left = room;
        for (Item item : pending) {
            Slice whole = whole(item);
            long cost = tokens.applyAsLong(whole.render(true));
            if (cost <= left) {
                batch.taken.add(new Taken(item, whole, item.result.content().length()));
                left -= cost;
            } else if (cost > room && batch.isEmpty()) {
                var part = nextPart(item, room, cost);
                part.ifPresent(batch.taken::add);
                left = 0;
            }
        }
        return batch;
    }

    /** Marks what the batch took as read. */
    void commit(Batch batch) {
        for (Taken t : batch.taken) {
            t.item.offset = t.newOffset;
            t.item.partsRead++;
            if (t.item.offset >= t.item.result.content().length()) {
                pending.remove(t.item);
            }
        }
    }

    private static Slice whole(Item item) {
        String text = item.remaining();
        if (item.partsRead == 0) {
            return new Slice(item.result.key(), item.result.tool(), text, 0, 0);
        }
        int part = item.partsRead + 1;
        return new Slice(item.result.key(), item.result.tool(), text, part, part);
    }

    /** The next part of an oversize result: as many lines as the room holds after the part's label. */
    private Optional<Taken> nextPart(Item item, long room, long remainingCost) {
        String rest = item.remaining();
        int part = item.partsRead + 1;
        long labelCost = tokens.applyAsLong(new Slice(item.result.key(), item.result.tool(), "", part, 99).render(true));
        long body = room - labelCost;
        if (body <= 0) {
            return Optional.empty();
        }
        int end = headLength(rest, body);
        if (end <= 0) {
            return Optional.empty();
        }
        int parts = item.partsRead + (int) Math.max(1, (remainingCost + body - 1) / body);
        var slice = new Slice(item.result.key(), item.result.tool(), rest.substring(0, end), part, Math.max(part, parts));
        return Optional.of(new Taken(item, slice, item.offset + end));
    }

    /**
     * The length of the longest head of {@code text} that holds at most {@code maxTokens} tokens, cut
     * at the last line break when it has one; a head with no line break (one very long line) is cut
     * at the character.
     */
    private int headLength(String text, long maxTokens) {
        int lo = 0;
        int hi = text.length();
        while (lo < hi) {
            int mid = (lo + hi + 1) >>> 1;
            if (tokens.applyAsLong(text.substring(0, mid)) <= maxTokens) {
                lo = mid;
            } else {
                hi = mid - 1;
            }
        }
        if (lo >= text.length()) {
            return lo;
        }
        int newline = text.lastIndexOf('\n', lo - 1);
        int cut = newline > 0 ? newline + 1 : lo;
        return cut > 0 && Character.isHighSurrogate(text.charAt(cut - 1)) ? cut - 1 : cut;
    }
}
