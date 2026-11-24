package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.eclipse.jgit.api.Git;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.ai.document.Document;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Set;
import java.util.stream.Collectors;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** Loading a repository over a real (local) git remote. */
class GitSourceLoaderTest {

    @TempDir
    Path temp;

    private Path remote;
    private Git git;

    @BeforeEach
    void remoteRepository() throws Exception {
        remote = temp.resolve("remotes/docs-repo");
        Files.createDirectories(remote);
        git = Git.init().setDirectory(remote.toFile()).setInitialBranch("main").call();
        write("README.md", "readme text");
        write("docs/guide.md", "guide text");
        write("docs/notes.bin", "not indexed");
        write("build/out.md", "excluded build output");
        write("big.md", "x".repeat(500));
        commit("first");
    }

    // No trailing slash: the repository name is the last segment of the URL.
    private String remoteUrl() {
        return "file://" + remote;
    }

    private void write(String name, String text) throws Exception {
        Path file = remote.resolve(name);
        Files.createDirectories(file.getParent());
        Files.writeString(file, text);
    }

    private void commit(String message) throws Exception {
        git.add().addFilepattern(".").call();
        git.commit().setMessage(message).setSign(false).call();
    }

    private IndexerConfig config(List<String> paths) {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.gitUrl()).thenReturn(remoteUrl());
        when(config.gitBranch()).thenReturn("main");
        when(config.gitPaths()).thenReturn(paths);
        when(config.workDir()).thenReturn(temp.resolve("work").toString());
        when(config.includePatterns()).thenReturn(List.of("*.md", "**/*.md"));
        when(config.excludePatterns()).thenReturn(List.of("build/**"));
        when(config.maxFileSize()).thenReturn(100);
        return config;
    }

    private static Set<String> paths(List<Document> documents) {
        return documents.stream().map(d -> (String) d.getMetadata().get("file_path")).collect(Collectors.toSet());
    }

    @Test
    void supportsOnlyTheGitType() {
        GitSourceLoader loader = new GitSourceLoader(config(null));
        assertTrue(loader.supports("git"));
        assertTrue(loader.supports("GIT"));
        assertFalse(loader.supports("url"));
    }

    @Test
    void clonesAndLoadsMatchingFilesSkippingExcludedAndOversizedOnes() {
        List<Document> documents = new GitSourceLoader(config(null)).load();

        assertEquals(Set.of("docs-repo/README.md", "docs-repo/docs/guide.md"), paths(documents));
        Document readme = documents.stream()
            .filter(d -> d.getMetadata().get("file_path").equals("docs-repo/README.md")).findFirst().orElseThrow();
        assertEquals("readme text", readme.getText());
        assertEquals("git", readme.getMetadata().get("source"));
        assertEquals(remoteUrl(), readme.getMetadata().get("repo_url"));
    }

    @Test
    void loadsOnlyTheRequestedPathsAndStripsATrailingGlob() {
        List<Document> documents = new GitSourceLoader(config(List.of("docs/**", "missing"))).load();

        assertEquals(Set.of("docs-repo/docs/guide.md"), paths(documents));
    }

    @Test
    void aSecondLoadPullsNewCommits() throws Exception {
        GitSourceLoader loader = new GitSourceLoader(config(null));
        loader.load();
        write("NEW.md", "added later");
        commit("second");

        assertTrue(paths(loader.load()).contains("docs-repo/NEW.md"));
    }

    @Test
    void loadRequiresAGitUrl() {
        IndexerConfig config = mock(IndexerConfig.class);
        assertThrows(IllegalArgumentException.class, () -> new GitSourceLoader(config).load());
        when(config.gitUrl()).thenReturn(" ");
        assertThrows(IllegalArgumentException.class, () -> new GitSourceLoader(config).load());
    }

    @Test
    void loadFailsWhenTheRemoteCannotBeCloned() {
        IndexerConfig config = config(null);
        when(config.gitUrl()).thenReturn("file://" + temp.resolve("no-such-remote.git"));

        assertThrows(IllegalStateException.class, () -> new GitSourceLoader(config).load());
    }
}
