package ai.kubemoot.agent.lease;

/**
 * Where leases live: one entry per key that exists while a holder owns the key. Taking a lease is a
 * create that fails while another holder's entry exists; keeping it is a compare-and-set on the
 * revision the holder last wrote; the store drops an entry that is not renewed within the lease.
 * The NATS KV bucket is the implementation ({@link KvLeaseStore}); tests use a fake with a clock.
 * The placement service holds one lease (its leader key), a coordinator one per thread it owns.
 */
public interface LeaseStore {

    /** Revision returned when the lease is held by another holder or the compare-and-set lost. */
    long NOT_HELD = -1L;

    /** Takes the lease on {@code key} if nobody holds it; the new revision, or {@link #NOT_HELD}. */
    long acquire(String key, String holder) throws Exception;

    /** Renews the lease on {@code key} if {@code revision} is still the latest write; the new revision, or {@link #NOT_HELD}. */
    long renew(String key, String holder, long revision) throws Exception;

    /** Gives the lease on {@code key} up if {@code revision} is still the latest write. */
    void release(String key, long revision) throws Exception;

    /** True while some holder's entry exists for {@code key}: the lease is held and has not lapsed. */
    boolean isHeld(String key) throws Exception;
}
