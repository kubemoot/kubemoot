package kubemoot.ai.mcpgateway.service;

import reactor.netty.http.client.PrematureCloseException;

import java.io.IOException;
import java.net.ConnectException;
import java.nio.channels.ClosedChannelException;
import java.util.function.Predicate;

/**
 * Classifies a failed HTTP exchange by what it says about the request. A refused connection
 * never reached the server. A connection the remote side closed or reset may have been used
 * before the request was written, so only requests that change nothing may be sent again.
 */
final class ConnectionFailures {

    private ConnectionFailures() {
    }

    /** True when the failure, or a cause of it, is a connection the remote side closed, reset, or broke. */
    static boolean isConnectionReset(Throwable failure) {
        return causeChainMatches(failure, t -> t instanceof PrematureCloseException
            || t instanceof ClosedChannelException
            || (t instanceof IOException && closedByPeer(t.getMessage())));
    }

    /** True when the connection was refused, so the request never reached the server. */
    static boolean isConnectRefused(Throwable failure) {
        return causeChainMatches(failure, t -> t instanceof ConnectException);
    }

    private static boolean closedByPeer(String message) {
        String text = String.valueOf(message);
        return text.contains("Connection reset") || text.contains("Broken pipe");
    }

    private static boolean causeChainMatches(Throwable failure, Predicate<Throwable> test) {
        for (Throwable t = failure; t != null; t = t.getCause()) {
            if (test.test(t)) {
                return true;
            }
        }
        return false;
    }
}
