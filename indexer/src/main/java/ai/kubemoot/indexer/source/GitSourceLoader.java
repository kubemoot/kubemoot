package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.eclipse.jgit.api.Git;
import org.eclipse.jgit.api.errors.GitAPIException;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.nio.file.*;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Loads documents from a Git repository.
 */
@Component
public class GitSourceLoader implements SourceLoader {

    private static final Logger logger = LoggerFactory.getLogger(GitSourceLoader.class);

    private final IndexerConfig config;

    public GitSourceLoader(IndexerConfig config) {
        this.config = config;
    }

    @Override
    public boolean supports(String sourceType) {
        return "git".equalsIgnoreCase(sourceType);
    }

    @Override
    public List<Document> load() {
        if (config.gitUrl() == null || config.gitUrl().isBlank()) {
            throw new IllegalArgumentException("KUBEMOOT_GIT_URL is required for git source");
        }

        Path workDir = Path.of(config.workDir());
        String repoName = extractRepoName(config.gitUrl());
        Path repoPath = workDir.resolve(repoName);

        try {
            // Clone or pull repository
            cloneOrPull(repoPath);

            // Load documents from specified paths
            List<String> pathsToIndex = config.gitPaths();
            if (pathsToIndex == null || pathsToIndex.isEmpty()) {
                pathsToIndex = List.of(".");
            }

            List<Document> documents = new ArrayList<>();
            for (String relPath : pathsToIndex) {
                indexRelPath(repoPath, repoName, relPath, documents);
            }

            logger.info("Loaded {} documents from git repo", documents.size());
            return documents;

        } catch (GitAPIException e) {
            throw new IllegalStateException("Failed to load git source", e);
        } catch (IOException e) {
            throw new java.io.UncheckedIOException("Failed to load git source", e);
        }
    }

    private void indexRelPath(Path repoPath, String repoName, String relPath, List<Document> documents)
            throws IOException {
        // Strip trailing glob patterns (e.g., "docs/**" → "docs")
        String cleanPath = relPath.replaceAll("/\\*\\*$", "").replaceAll("\\*\\*$", "");
        if (cleanPath.isEmpty()) cleanPath = ".";
        Path indexPath = cleanPath.equals(".") ? repoPath : repoPath.resolve(cleanPath);
        if (Files.exists(indexPath)) {
            String sourceName = cleanPath.equals(".") ? repoName : repoName + "/" + cleanPath;
            documents.addAll(loadFromPath(indexPath, sourceName));
        } else {
            logger.warn("Path not found in repo: {} (resolved: {})", relPath, cleanPath);
        }
    }

    private void cloneOrPull(Path repoPath) throws GitAPIException, IOException {
        if (Files.exists(repoPath)) {
            logger.info("Pulling latest for {}...", repoPath.getFileName());
            try (Git git = Git.open(repoPath.toFile())) {
                git.fetch().call();
                git.checkout().setName(config.gitBranch()).call();
                git.pull().call();
            } catch (Exception e) {
                logger.warn("Pull failed, resetting to origin...");
                try (Git git = Git.open(repoPath.toFile())) {
                    git.reset()
                        .setMode(org.eclipse.jgit.api.ResetCommand.ResetType.HARD)
                        .setRef("origin/" + config.gitBranch())
                        .call();
                }
            }
        } else {
            logger.info("Cloning {} (branch: {})...", config.gitUrl(), config.gitBranch());
            Files.createDirectories(repoPath.getParent());
            Git.cloneRepository()
                .setURI(config.gitUrl())
                .setDirectory(repoPath.toFile())
                .setBranch(config.gitBranch())
                .setDepth(1)
                .call()
                .close();
        }
    }

    private List<Document> loadFromPath(Path basePath, String sourceName) throws IOException {
        List<Document> documents = new ArrayList<>();
        PathMatcher[] includeMatchers = config.includePatterns().stream()
            .map(p -> FileSystems.getDefault().getPathMatcher("glob:" + p))
            .toArray(PathMatcher[]::new);
        PathMatcher[] excludeMatchers = config.excludePatterns().stream()
            .map(p -> FileSystems.getDefault().getPathMatcher("glob:" + p))
            .toArray(PathMatcher[]::new);

        Files.walkFileTree(basePath, new SimpleFileVisitor<>() {
            @Override
            public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) {
                Path relPath = basePath.relativize(file);

                if (!matchesAny(includeMatchers, relPath)) return FileVisitResult.CONTINUE;
                if (matchesAny(excludeMatchers, relPath)) return FileVisitResult.CONTINUE;

                if (attrs.size() > config.maxFileSize()) {
                    logger.debug("Skipping large file: {}", file);
                    return FileVisitResult.CONTINUE;
                }

                addFileDocument(documents, file, sourceName, relPath);
                return FileVisitResult.CONTINUE;
            }
        });

        logger.info("Loaded {} documents from {}", documents.size(), sourceName);
        return documents;
    }

    private static boolean matchesAny(PathMatcher[] matchers, Path relPath) {
        for (PathMatcher m : matchers) {
            if (m.matches(relPath)) {
                return true;
            }
        }
        return false;
    }

    private void addFileDocument(List<Document> documents, Path file, String sourceName, Path relPath) {
        try {
            String content = Files.readString(file);
            Map<String, Object> metadata = new HashMap<>();
            metadata.put("source", "git");
            metadata.put("file_path", sourceName + "/" + relPath);
            metadata.put("repo_url", config.gitUrl());
            documents.add(new Document(content, metadata));
        } catch (IOException e) {
            logger.error("Failed to read file: {}", file, e);
        }
    }

    private String extractRepoName(String gitUrl) {
        String name = gitUrl;
        if (name.endsWith(".git")) {
            name = name.substring(0, name.length() - 4);
        }
        int lastSlash = name.lastIndexOf('/');
        if (lastSlash >= 0) {
            name = name.substring(lastSlash + 1);
        }
        return name;
    }
}
