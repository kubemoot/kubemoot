package ai.kubemoot.agent.mcp;

import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The thread of the running turn reaches the tools that read stored results; no other tool gets it. */
class WorkspaceScopeTest {

    private static final String PREFIX = "crew-homelab-pilot/homelab-pilot/0071d018-24cb-4532-a560-fc12a788e654/";

    @Test
    void theReadingToolsGetThePrefixInMetaAndOtherToolsDoNot() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            for (String tool : new String[] {"workspace_query", "artifact_head", "artifact_tail", "artifact_grep",
                    "artifact_count", "artifact_rows", "artifact_select", "artifact_jq", "execute_code",
                    "workspace-access.workspace_query", "workspace_head", "workspace_tail", "workspace_grep",
                    "workspace_list", "workspace_describe", "workspace-access.workspace_head"}) {
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
    void codeIsSentAsTheModelWroteIt() {
        // The sandbox shows the program its own discussion at the workspace path, so the path needs no rewriting.
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            assertTrue(WorkspaceScope.metaFor("execute_code").containsKey(WorkspaceScope.META_KEY));
        }
    }
}
