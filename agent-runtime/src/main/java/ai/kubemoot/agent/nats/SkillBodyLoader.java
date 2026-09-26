package ai.kubemoot.agent.nats;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Collection;

/**
 * Loads skill ADL bodies from the skills ConfigMap mount.
 *
 * The mount path is configured by the operator via KUBEMOOT_SKILLS_DIR
 * (default: /app/config/skills). Each skill is a file named
 * {@code <skill-name>.txt} under that directory.
 *
 * Progressive disclosure: ALL skill bodies are present on disk (from the
 * per-crew ConfigMap). Only the coordinator-selected names are loaded per
 * turn, so context stays bounded to what is needed for the question.
 *
 * Robustness contract: a missing or unreadable file is SKIPPED with a log
 * warning and never fails the caller's turn. Malformed names (path separators,
 * null bytes) are rejected before the filesystem is touched.
 *
 * GraalVM native-safe: uses java.nio.file.Files directly; no reflection.
 */
class SkillBodyLoader {

    private static final Logger log = LoggerFactory.getLogger(SkillBodyLoader.class);

    static final String DEFAULT_SKILLS_DIR = "/app/config/skills";

    private final String skillsDir;

    SkillBodyLoader(String skillsDir) {
        this.skillsDir = skillsDir != null && !skillsDir.isBlank()
                ? skillsDir
                : DEFAULT_SKILLS_DIR;
    }

    /**
     * Load the bodies of the named skills and return them concatenated with
     * a header, ready to prepend into the specialist's LLM context.
     *
     * Returns an empty string when {@code skillNames} is null or empty (the
     * ZERO guarantee: the baseline turn is byte-identical to pre-skills).
     */
    String load(Collection<String> skillNames) {
        if (skillNames == null || skillNames.isEmpty()) {
            return "";
        }
        var sb = new StringBuilder();
        for (String name : skillNames) {
            String body = readSkillFile(name);
            if (body == null) {
                continue;
            }
            if (!sb.isEmpty()) {
                sb.append("\n\n");
            }
            sb.append("--- Skill: ").append(name).append(" ---\n");
            sb.append(body);
        }
        if (sb.isEmpty()) {
            return "";
        }
        return "Skills context for this discussion:\n\n" + sb + "\n\n";
    }

    /**
     * Read a single skill file. Returns null when the name is invalid (contains
     * path separators or is blank) or when the file is missing or unreadable.
     * Null causes the caller to skip this skill silently.
     */
    private String readSkillFile(String name) {
        if (name == null || name.isBlank()) {
            log.warn("Ignoring blank or null skill name");
            return null;
        }
        if (name.contains("/") || name.contains("\\") || name.contains("\0")) {
            log.warn("Ignoring malformed skill name: '{}'", name);
            return null;
        }
        Path path = Path.of(skillsDir, name + ".txt");
        try {
            return Files.readString(path);
        } catch (IOException e) {
            log.warn("Skill file not found or unreadable, skipping: {} ({})", path, e.getMessage());
            return null;
        }
    }
}
