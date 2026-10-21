package ai.kubemoot.placement;

/**
 * Where the lease lives: one entry that exists while a leader holds it. Taking it is a create that
 * fails while another holder's entry exists; keeping it is a compare-and-set on the revision the
 * holder last wrote; the store drops the entry itself when it is not renewed within the lease. The
 * leader bucket is the implementation ({@link KvLeaseStore}); tests use a fake with a clock.
 */
interface LeaseStore {

    /** Revision returned when the lease is held by another holder or the compare-and-set lost. */
    long NOT_HELD = -1L;

    /** Takes the lease if nobody holds it; the new revision, or {@link #NOT_HELD}. */
    long acquire(String holder) throws Exception;

    /** Renews the lease if {@code revision} is still the latest write; the new revision, or {@link #NOT_HELD}. */
    long renew(String holder, long revision) throws Exception;

    /** Gives the lease up if {@code revision} is still the latest write. */
    void release(long revision) throws Exception;
}
