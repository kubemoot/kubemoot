package ai.kubemoot.agent.logging;

import org.jboss.logmanager.ExtLogRecord;

import java.util.ArrayList;
import java.util.List;
import java.util.logging.Handler;
import java.util.logging.Level;
import java.util.logging.LogRecord;
import java.util.logging.Logger;

/**
 * Collects everything the code under test logs, at every level, through the root logger.
 * Close it to restore the logger. Used by the tests that hold the logs free of content.
 */
public final class LogCapture implements AutoCloseable {

    /** The categories whose level is raised: root, and Kubemoot's own packages, which may set their own level. */
    private static final String[] CATEGORIES = {"", "ai.kubemoot", "kubemoot.ai"};

    private final Logger root = Logger.getLogger("");
    private final Level[] before = new Level[CATEGORIES.length];
    private final List<String> lines = new ArrayList<>();
    private final Handler handler = new Handler() {
        @Override
        public void publish(LogRecord record) {
            StringBuilder line = new StringBuilder(record instanceof ExtLogRecord ext
                    ? ext.getFormattedMessage() : String.valueOf(record.getMessage()));
            if (record.getThrown() != null) {
                line.append(' ').append(record.getThrown());
            }
            synchronized (lines) {
                lines.add(line.toString());
            }
        }

        @Override
        public void flush() {
        }

        @Override
        public void close() {
        }
    };

    public LogCapture() {
        for (int i = 0; i < CATEGORIES.length; i++) {
            Logger category = Logger.getLogger(CATEGORIES[i]);
            before[i] = category.getLevel();
            category.setLevel(Level.ALL);
        }
        root.addHandler(handler);
    }

    /** Everything logged so far, one line per record, formatted with its arguments. */
    public String text() {
        synchronized (lines) {
            return String.join("\n", lines);
        }
    }

    @Override
    public void close() {
        root.removeHandler(handler);
        for (int i = 0; i < CATEGORIES.length; i++) {
            Logger.getLogger(CATEGORIES[i]).setLevel(before[i]);
        }
    }
}
