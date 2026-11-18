package ai.kubemoot.agent.mcp;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.io.IOException;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The registry holds the same names as operator/pkg/workspacetools, which runs the same fixture. */
class WorkspaceToolsTest {

    private static JsonNode fixture() throws IOException {
        return new ObjectMapper().readTree(new File("../test/fixtures/workspace-tools.json"));
    }

    @Test
    void theMountPathsAreTheSharedFixturesAndNotTheBuildpacksAppDir() throws IOException {
        JsonNode f = new ObjectMapper().readTree(new File("../test/fixtures/workspace-mount.json"));
        assertEquals(f.get("mountPath").asText(), WorkspaceTools.MOUNT);
        assertEquals(f.get("legacyMountPath").asText(), WorkspaceTools.LEGACY_MOUNT);
        String cnb = f.get("cnbAppDir").asText();
        assertTrue(!WorkspaceTools.MOUNT.equals(cnb) && !WorkspaceTools.MOUNT.startsWith(cnb + "/"));
    }

    @Test
    void theNamesAreTheSharedFixturesNames() throws IOException {
        JsonNode f = fixture();
        Set<String> read = new HashSet<>();
        f.get("readTools").forEach(n -> read.add(n.asText()));
        Map<String, String> legacy = new HashMap<>();
        f.get("legacy").fields().forEachRemaining(e -> legacy.put(e.getKey(), e.getValue().asText()));

        assertEquals(read, WorkspaceTools.currentNames());
        assertEquals(legacy, WorkspaceTools.legacyNames());
    }

    @Test
    void everyNameIsAWorkspaceToolAndAnOldNameMapsToItsReplacement() throws IOException {
        for (JsonNode n : fixture().get("readTools")) {
            assertTrue(WorkspaceTools.isWorkspace(n.asText()), n.asText());
            assertTrue(WorkspaceTools.isWorkspace("server." + n.asText()), "a server prefix is ignored");
        }
        fixture().get("legacy").fields().forEachRemaining(e -> {
            assertEquals(e.getValue().asText(), WorkspaceTools.canonical(e.getKey()));
            assertTrue(WorkspaceTools.isWorkspace(e.getKey()), e.getKey());
        });
        assertFalse(WorkspaceTools.isWorkspace("pods_list"));
    }
}
