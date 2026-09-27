package ai.kubemoot.agent.provider;

/**
 * What switching models costs on the model server, in the terms the placement
 * cost model reasons in.
 *
 * @param coldLoadMiBPerSecond throughput of loading a model that is not resident
 * @param releaseSeconds       time to release a resident model's memory
 * @param wakeMiBPerSecond     throughput of bringing a released model back; equal to
 *                             the cold-load throughput when a release fully unloads
 */
public record SwitchCostProfile(double coldLoadMiBPerSecond, double releaseSeconds, double wakeMiBPerSecond) {

    /** Seconds to load a model of {@code footprintMiB} that is not resident. */
    public double coldLoadSeconds(long footprintMiB) {
        return Math.max(0L, footprintMiB) / coldLoadMiBPerSecond;
    }

    /** Seconds to bring a released model of {@code footprintMiB} back. */
    public double wakeSeconds(long footprintMiB) {
        return Math.max(0L, footprintMiB) / wakeMiBPerSecond;
    }
}
