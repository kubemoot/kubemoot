package ai.kubemoot.agent.nats;

import java.time.Instant;
import java.util.List;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Proposal store for onboarding consent flow.
 *
 * Gap detection is now handled by the coordinator's LLM (it reads the thread
 * and naturally suggests onboarding when no tooler can help). This class
 * only stores pending proposals so the consent handler can find them.
 */
public class GapDetector {

    // Age (seconds) after which a pending onboarding proposal is dropped during cleanup.
    private static final long PROPOSAL_TTL_SECONDS = 3600;

    private final ConcurrentHashMap<String, ProposalState> pendingProposals = new ConcurrentHashMap<>();

    public GapDetector() {
        // No initialization needed: pendingProposals is initialized at its declaration.
    }

    // Legacy constructor - timeout ignored since gap detection moved to coordinator LLM.
    public GapDetector(int ignored) {
        // Intentionally empty: the timeout parameter is retained for binary compatibility only.
    }

    /**
     * Store a pending onboarding proposal keyed by domain.
     */
    public void storeProposal(String domain, String serverName, String registryUrl) {
        pendingProposals.put(domain.toLowerCase(), new ProposalState(domain, serverName, registryUrl, List.of()));
    }

    /**
     * Store a pending onboarding proposal with user-provided documentation URLs.
     */
    public void storeProposal(String domain, String serverName, String registryUrl, List<String> docUrls) {
        pendingProposals.put(domain.toLowerCase(), new ProposalState(domain, serverName, registryUrl, docUrls != null ? docUrls : List.of()));
    }

    /**
     * Check if there's a pending proposal matching the given domain keyword.
     */
    public ProposalState findProposal(String query) {
        String queryLower = query.toLowerCase();
        for (var entry : pendingProposals.entrySet()) {
            if (queryLower.contains(entry.getKey())) {
                return entry.getValue();
            }
        }
        return null;
    }

    /**
     * Remove a pending proposal.
     */
    public void removeProposal(String domain) {
        pendingProposals.remove(domain.toLowerCase());
    }

    /**
     * Periodic cleanup of old entries to prevent memory growth.
     */
    public void cleanup() {
        long now = Instant.now().getEpochSecond();
        pendingProposals.entrySet().removeIf(e ->
                now - e.getValue().proposedAt.getEpochSecond() > PROPOSAL_TTL_SECONDS);
    }

    public record ProposalState(String domain, String serverName, String registryUrl, List<String> docUrls, Instant proposedAt) {
        public ProposalState(String domain, String serverName, String registryUrl, List<String> docUrls) {
            this(domain, serverName, registryUrl, docUrls != null ? docUrls : List.of(), Instant.now());
        }
    }
}
