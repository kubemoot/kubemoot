package ai.kubemoot.agent.mcp;

import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The thread of the running turn reaches the tools that read stored results; no other tool gets it. */
class WorkspaceScopeTest {

    private static final String PREFIX = "crew-homelab-pilot/homelab-pilot/0071d018-24cb-4532-a560-fc12a788e654/";
    private static final String SHORT = "/artifacts/k8s-workloads/002-resources_list";

    @Test
    void theReadingToolsGetThePrefixInMetaAndOtherToolsDoNot() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            for (String tool : new String[] {"workspace_query", "artifact_head", "artifact_tail", "artifact_grep",
                    "artifact_count", "artifact_rows", "artifact_select", "artifact_jq", "execute_code",
                    "artifact-access.workspace_query"}) {
                assertEquals(Map.of(WorkspaceScope.META_KEY, PREFIX), WorkspaceScope.metaFor(tool), tool);
            }
            for (String tool : new String[] {"resources_list", "pods_list", "execute_query", "validate_code",
                    "my_artifact_tool"}) {
                assertTrue(WorkspaceScope.metaFor(tool).isEmpty(), tool);
            }
        }
    }

    @Test
    void outsideAThreadNothingIsSent() {
        assertTrue(WorkspaceScope.metaFor("workspace_query").isEmpty());
        try (var scope = WorkspaceScope.bind(null)) {
            assertTrue(WorkspaceScope.metaFor("workspace_query").isEmpty());
            assertTrue(WorkspaceScope.current().isEmpty());
        }
        try (var scope = WorkspaceScope.bind("  ")) {
            assertTrue(WorkspaceScope.metaFor("workspace_query").isEmpty(), "a blank prefix is no thread");
        }
    }

    @Test
    void closingTheScopeRestoresTheOuterOne() {
        try (var outer = WorkspaceScope.bind("a/b/c/")) {
            try (var inner = WorkspaceScope.bind(PREFIX)) {
                assertEquals(Optional.of(PREFIX), WorkspaceScope.current());
            }
            assertEquals(Optional.of("a/b/c/"), WorkspaceScope.current());
        }
        assertTrue(WorkspaceScope.current().isEmpty());
    }

    @Test
    void aShortArtifactPathInCodeIsWrittenOutInTheThread() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            var args = Map.<String, Object>of("language", "python",
                    "code", "import csv\nrows = open('" + SHORT + "').read()\nprint(len(rows))");

            var sent = WorkspaceScope.argumentsFor("execute_code", args);

            assertEquals("import csv\nrows = open('/artifacts/" + PREFIX + "k8s-workloads/002-resources_list').read()"
                    + "\nprint(len(rows))", sent.get("code"));
            assertEquals("python", sent.get("language"), "the other arguments are kept");
        }
    }

    @Test
    void aPatternAndSeveralPathsInOneProgramAreAllExpanded() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            var args = Map.<String, Object>of("code",
                    "glob('/artifacts/k8s-workloads/*-resources_list'); open('/artifacts/k8s-nodes/001-nodes_list'); "
                            + "open('/artifacts/k8s-nodes/*')");

            String code = (String) WorkspaceScope.argumentsFor("execute_code", args).get("code");

            assertEquals("glob('/artifacts/" + PREFIX + "k8s-workloads/*-resources_list'); open('/artifacts/" + PREFIX
                    + "k8s-nodes/001-nodes_list'); open('/artifacts/" + PREFIX + "k8s-nodes/*')", code);
        }
    }

    @Test
    void aPathThatAlreadyNamesAThreadIsLeftAlone() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            var args = Map.<String, Object>of("code", "open('/artifacts/" + PREFIX + "k8s-workloads/002-resources_list')");

            assertSame(args, WorkspaceScope.argumentsFor("execute_code", args));
        }
    }

    @Test
    void aLongPathWhoseCrewNameLooksLikeACallFileIsNotRewritten() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            var args = Map.<String, Object>of("code", "open('/artifacts/ns/123-crew/thread/agent/001-x')");

            assertSame(args, WorkspaceScope.argumentsFor("execute_code", args));
        }
    }

    @Test
    void codeWithoutAnArtifactPathAndOtherToolsAreUntouched() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            var plain = Map.<String, Object>of("code", "print(1+1)");
            var other = Map.<String, Object>of("code", SHORT);
            var noCode = Map.<String, Object>of("language", "bash");

            assertSame(plain, WorkspaceScope.argumentsFor("execute_code", plain));
            assertSame(other, WorkspaceScope.argumentsFor("validate_code", other), "only execute_code runs against files");
            assertSame(noCode, WorkspaceScope.argumentsFor("execute_code", noCode));
            assertSame(null, WorkspaceScope.argumentsFor("execute_code", null));
        }
    }

    @Test
    void codeIsNotRewrittenOutsideAThread() {
        var args = Map.<String, Object>of("code", "open('" + SHORT + "')");

        assertSame(args, WorkspaceScope.argumentsFor("execute_code", args));
    }

    @Test
    void aNonStringCodeArgumentIsLeftForTheToolToRefuse() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            var args = Map.<String, Object>of("code", 42);

            assertSame(args, WorkspaceScope.argumentsFor("execute_code", args));
        }
    }

    @Test
    void replacementTextWithDollarsAndBackslashesIsTakenAsIs() {
        String code = WorkspaceScope.expandPaths("open('" + SHORT + "')", "ns/crew/th$1\\x/");

        assertEquals("open('/artifacts/ns/crew/th$1\\x/k8s-workloads/002-resources_list')", code);
    }
}
