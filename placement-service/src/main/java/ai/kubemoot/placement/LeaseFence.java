package ai.kubemoot.placement;

import java.util.concurrent.atomic.AtomicReference;
import java.util.function.BooleanSupplier;

/**
 * Whether this replica still holds the lease, as the leader election reports it. The ticket table
 * answers and the mirror writes only while the fence says so, so a deposed leader does nothing a
 * newer leader could be hurt by. Until a fence is set the replica counts as the holder.
 */
final class LeaseFence {

    private final AtomicReference<BooleanSupplier> source = new AtomicReference<>(() -> true);

    /** From now on the lease is held when {@code held} says so. */
    void set(BooleanSupplier held) {
        source.set(held);
    }

    /** True while this replica holds the lease. */
    boolean held() {
        return source.get().getAsBoolean();
    }
}
