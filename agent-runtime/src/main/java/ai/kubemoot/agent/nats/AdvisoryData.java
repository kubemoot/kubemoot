package ai.kubemoot.agent.nats;

import java.util.List;

/**
 * Parsed advisory data from the coordinator's inline advisory generation.
 * Contains technology identifiers and brief wisdom context.
 */
public record AdvisoryData(List<String> technologies, String wisdom) {

    public AdvisoryData {
        if (technologies == null) technologies = List.of();
        if (wisdom == null) wisdom = "";
    }
}
