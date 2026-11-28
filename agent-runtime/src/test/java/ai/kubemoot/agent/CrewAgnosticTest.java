package ai.kubemoot.agent;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import ai.kubemoot.agent.chat.ToolCallFailure;
import ai.kubemoot.agent.config.AgentProperties;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;

/**
 * Kubemoot teaches crews to fish and never fishes for them: the runtime keeps the moot's protocol
 * (phases, signals, tool-call plumbing, workspace) and carries no coaching. These tests pin that.
 *
 * <ul>
 *   <li>The coaching flags and failure type are gone.</li>
 *   <li>No string literal in the runtime's Java sources, and no text in its ADL prompts or
 *       properties, names a tool, an MCP server, a crew, an agent or a domain (Kubernetes,
 *       Prometheus, accelerator model names). The docs name no removed flag. The scan reads one
 *       literal at a time: a text built by joining literals with {@code +} or by formatting is
 *       checked per piece, so a term split across two pieces is not caught.</li>
 * </ul>
 */
class CrewAgnosticTest {

    private static final Path MAIN = Path.of("src/main/java");

    /** Config methods and failure types of mechanisms that were removed because they were coaching. */
    private static final Set<String> REMOVED_FLAGS = Set.of(
            "metricsDrillContract", "computeContract", "computeGrounding", "synthesisCompletenessRetries");

    @Test
    void theCoachingFlagsAreGone() {
        var discuss = Arrays.stream(AgentProperties.Discuss.class.getMethods())
                .map(java.lang.reflect.Method::getName).collect(Collectors.toSet());
        for (String flag : REMOVED_FLAGS) {
            assertFalse(discuss.contains(flag), flag + " must not return: it switched coaching on");
        }
    }

    @Test
    void theCoachingEnvironmentVariablesAreGone() throws IOException {
        var names = List.of("KUBEMOOT_DISCUSS_METRICS_DRILL_CONTRACT", "KUBEMOOT_DISCUSS_COMPUTE_CONTRACT",
                "KUBEMOOT_DISCUSS_COMPUTE_GROUNDING", "KUBEMOOT_DISCUSS_SYNTHESIS_COMPLETENESS_RETRIES");
        for (Path file : mainFiles(".java", ".properties")) {
            String text = Files.readString(file);
            for (String name : names) {
                assertFalse(text.contains(name), name + " is still named in " + file);
            }
        }
    }

    @Test
    void theComputeContractFailureTypeIsGone() {
        var types = Arrays.stream(ToolCallFailure.FailureType.values()).map(Enum::name).collect(Collectors.toSet());
        assertFalse(types.contains("COMPUTE_CONTRACT_UNSATISFIED"));
    }

    /**
     * Terms that steer an agent toward one tool or one domain. Matched case-insensitively against each
     * string literal of the main sources. The names of the moot's own tools (the workspace and the
     * signal tools) are not listed: they are the protocol, and the code takes them from one registry.
     */
    private static final Pattern FORBIDDEN = Pattern.compile(String.join("|",
            "kubectl", "kubernetes", "prometheus", "promql", "label selector", "OLLAMA_[A-Z_]+",
            "homelab", "proxmox", "\\bgpu\\b", "\\brtx\\b", "nvidia", "qwen",
            "list_metrics", "get_metric_metadata", "execute_query", "execute_range_query",
            "resources_list", "pods_list", "fetch-mcp", "execute_code"), Pattern.CASE_INSENSITIVE);

    /**
     * The matched terms a file may carry, each with the reason. An entry is a file and the exact
     * lower-case token, never a whole file, and fails the test once it stops matching.
     */
    private static final Map<String, Map<String, String>> ALLOWED = Map.of(
            "mcp/WorkspaceTools.java", Map.of("execute_code",
                    "the registry of the moot's own workspace tools; the one place the runtime names the"
                            + " code-sandbox tool, and every prompt takes it from here"),
            "chat/ResultType.java", Map.of("kubectl",
                    "the media-type profile token 'kubectl-table' is a wire contract shared with the"
                            + " workspace-access server and its fixtures; renaming it changes both components"),
            "chat/NativeSelfTest.java", Map.of("kubectl",
                    "a start-up check that the native image detects that same media-type profile token"),
            // Follow-up card: "Onboarding and RTFM Prompts Become PromptModules".
            "nats/RtfmSubscriber.java", Map.of("fetch-mcp",
                    "Kubemoot's own documentation-indexing feature names the fetch tool; it becomes a"
                            + " PromptModule of the onboarding agent (card above)"),
            "application.properties", Map.of(
                    "ollama_base_url", "the engine's endpoint setting; configuration, no text reaches a model",
                    "ollama_chat_model_model_id", "the engine's default model setting; configuration",
                    "ollama_timeout", "the engine's timeout setting; configuration",
                    "qwen", "the default model id when no agent sets one; configuration",
                    "prometheus", "the metrics exporter of the runtime itself; no text reaches a model"),
            "nats/CrewScope.java", Map.of("kubernetes",
                    "the service-account path and environment names read to learn the runtime's own"
                            + " namespace; configuration, no text reaches a model"));

    @Test
    void noLiteralInTheRuntimeNamesAToolACrewOrADomain() throws IOException {
        var hits = new ArrayList<String>();
        var used = new java.util.TreeSet<String>();
        for (Path file : mainFiles(".java", ".adl", ".properties")) {
            String rel = relative(file);
            String text = Files.readString(file);
            var pieces = piecesOf(file, text);
            for (String piece : pieces) {
                var m = FORBIDDEN.matcher(piece);
                while (m.find()) {
                    String token = m.group().toLowerCase(java.util.Locale.ROOT);
                    if (ALLOWED.getOrDefault(rel, Map.of()).containsKey(token)) {
                        used.add(rel + " " + token);
                    } else {
                        hits.add(rel + " [" + token + "]: " + abbreviate(piece));
                    }
                }
            }
        }
        assertTrue(hits.isEmpty(), "the runtime must stay crew agnostic; these texts name a tool, a crew or"
                + " a domain:\n  " + String.join("\n  ", hits));
        var unused = new java.util.TreeSet<String>();
        ALLOWED.forEach((file, tokens) -> tokens.keySet().forEach(t -> unused.add(file + " " + t)));
        unused.removeAll(used);
        assertEquals(Set.of(), unused, "allowlist entries that no longer match anything must be removed");
    }

    @Test
    void theDocsNameNoRemovedFlag() throws IOException {
        var removed = List.of("METRICS_DRILL", "COMPUTE_CONTRACT", "COMPUTE_GROUNDING", "SYNTHESIS_COMPLETENESS");
        try (Stream<Path> walk = Files.walk(Path.of("../docs"))) {
            for (Path doc : walk.filter(p -> p.toString().endsWith(".md")).toList()) {
                String text = Files.readString(doc);
                for (String name : removed) {
                    assertFalse(text.contains(name), name + " is still named in " + doc);
                }
            }
        }
    }

    /** The texts to scan: the literals of Java, the settings (not the comments) of properties, all of an ADL file. */
    private static List<String> piecesOf(Path file, String text) {
        if (file.toString().endsWith(".java")) {
            return literals(text);
        }
        if (file.toString().endsWith(".properties")) {
            return text.lines().filter(l -> !l.stripLeading().startsWith("#")).toList();
        }
        return List.of(text);
    }

    private static String relative(Path file) {
        String p = file.toString().replace('\\', '/');
        int at = p.indexOf("ai/kubemoot/agent/");
        return at >= 0 ? p.substring(at + "ai/kubemoot/agent/".length()) : p.substring(p.indexOf("resources/") + 10);
    }

    @Test
    void theScanSeesALiteralInCodeAndSkipsComments() {
        String src = "class A { // \"kubectl in a line comment\"\n"
                + "  /* \"prometheus in a block comment\" */\n"
                + "  String a = \"list_metrics\"; char c = '\"'; String t = \"\"\"\n    homelab\n    \"\"\"; }";
        assertEquals(List.of("list_metrics", "\n    homelab\n    "), literals(src).stream()
                .filter(s -> !s.isEmpty()).collect(Collectors.toList()));
    }

    private static List<Path> mainFiles(String... suffixes) throws IOException {
        var roots = List.of(MAIN, Path.of("src/main/resources"));
        var files = new ArrayList<Path>();
        for (Path root : roots) {
            if (!Files.isDirectory(root)) {
                continue;
            }
            try (Stream<Path> walk = Files.walk(root)) {
                walk.filter(Files::isRegularFile)
                        .filter(p -> Arrays.stream(suffixes).anyMatch(s -> p.toString().endsWith(s)))
                        .forEach(files::add);
            }
        }
        return files;
    }

    private static String abbreviate(String s) {
        String one = s.replace('\n', ' ').strip();
        return one.length() > 100 ? one.substring(0, 100) + "..." : one;
    }

    /** The string literals of Java source, comments excluded; text blocks are returned as one literal. */
    static List<String> literals(String src) {
        var out = new ArrayList<String>();
        int i = 0;
        while (i < src.length()) {
            i = step(src, i, out);
        }
        return out;
    }

    /** Reads the token at {@code i}: records it when it is a literal, and returns the index after it. */
    private static int step(String src, int i, List<String> out) {
        if (src.startsWith("//", i)) {
            return skipTo(src, "\n", i);
        }
        if (src.startsWith("/*", i)) {
            return skipTo(src, "*/", i) + 2;
        }
        if (src.startsWith("\"\"\"", i)) {
            int end = skipTo(src, "\"\"\"", i + 3);
            out.add(src.substring(i + 3, end));
            return end + 3;
        }
        if (src.charAt(i) == '"') {
            int end = endOfString(src, i + 1);
            out.add(src.substring(i + 1, end));
            return end + 1;
        }
        if (src.charAt(i) == '\'') {
            return i + (src.startsWith("'\\", i) ? 4 : 3);
        }
        return i + 1;
    }

    private static int endOfString(String src, int from) {
        int end = from;
        while (end < src.length() && src.charAt(end) != '"') {
            end += src.charAt(end) == '\\' ? 2 : 1;
        }
        return Math.min(end, src.length());
    }

    private static int skipTo(String src, String marker, int from) {
        int at = src.indexOf(marker, from);
        return at < 0 ? src.length() : at;
    }
}
