package kubemoot.ai.mcpgateway.config;

import org.junit.jupiter.api.Test;
import org.springframework.mock.web.MockFilterChain;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Plain unit tests of the port split and the bearer check, with no server. */
class AdminAccessFilterTest {

    private static final int ADMIN = 8081;
    private static final int TOOLS = 8080;
    private static final String TOKEN = "s3cret-token";

    private final AdminAccessFilter filter = new AdminAccessFilter(ADMIN, TOKEN);

    private record Result(int status, boolean passed, String challenge) { }

    private Result run(AdminAccessFilter f, String method, String path, int port, String auth) throws Exception {
        MockHttpServletRequest request = new MockHttpServletRequest(method, path);
        request.setServletPath(path);
        request.setLocalPort(port);
        if (auth != null) {
            request.addHeader("Authorization", auth);
        }
        MockHttpServletResponse response = new MockHttpServletResponse();
        MockFilterChain chain = new MockFilterChain();
        f.doFilter(request, response, chain);
        return new Result(response.getStatus(), chain.getRequest() != null, response.getHeader("WWW-Authenticate"));
    }

    @Test
    void noTokenIsRefused() throws Exception {
        Result r = run(filter, "POST", "/admin/servers", ADMIN, null);
        assertEquals(401, r.status());
        assertFalse(r.passed());
        assertNotNull(r.challenge());
    }

    @Test
    void wrongTokenIsRefused() throws Exception {
        assertEquals(401, run(filter, "POST", "/admin/servers", ADMIN, "Bearer nope").status());
        assertEquals(401, run(filter, "POST", "/admin/servers", ADMIN, "Bearer " + TOKEN + "x").status());
        assertEquals(401, run(filter, "POST", "/admin/servers", ADMIN, "Bearer ").status());
        assertEquals(401, run(filter, "POST", "/admin/servers", ADMIN, "Basic " + TOKEN).status());
    }

    @Test
    void rightTokenPasses() throws Exception {
        Result r = run(filter, "POST", "/admin/servers", ADMIN, "Bearer " + TOKEN);
        assertTrue(r.passed());
        assertEquals(200, r.status());
        assertTrue(run(filter, "DELETE", "/admin/servers/a", ADMIN, "bearer " + TOKEN).passed());
    }

    @Test
    void unconfiguredTokenRefusesEveryone() throws Exception {
        AdminAccessFilter open = new AdminAccessFilter(ADMIN, "");
        assertEquals(401, run(open, "POST", "/admin/servers", ADMIN, "Bearer ").status());
        assertEquals(401, run(open, "POST", "/admin/servers", ADMIN, null).status());
    }

    @Test
    void adminPortServesOnlyAdminRoutes() throws Exception {
        assertEquals(404, run(filter, "GET", "/tools", ADMIN, "Bearer " + TOKEN).status());
        assertEquals(404, run(filter, "GET", "/actuator/health", ADMIN, "Bearer " + TOKEN).status());
    }

    @Test
    void toolPortKeepsToolListsOpen() throws Exception {
        assertTrue(run(filter, "GET", "/admin/tools", TOOLS, null).passed());
        assertTrue(run(filter, "GET", "/admin/servers/a/tools", TOOLS, null).passed());
        assertTrue(run(filter, "GET", "/admin/search/tools", TOOLS, null).passed());
        assertTrue(run(filter, "GET", "/tools", TOOLS, null).passed());
    }

    @Test
    void toolPortDoesNotServeMutatingAdminRoutes() throws Exception {
        assertEquals(404, run(filter, "POST", "/admin/servers", TOOLS, "Bearer " + TOKEN).status());
        assertEquals(404, run(filter, "DELETE", "/admin/servers/a", TOOLS, null).status());
        assertEquals(404, run(filter, "GET", "/admin/servers", TOOLS, null).status());
        assertEquals(404, run(filter, "GET", "/admin/feedback", TOOLS, null).status());
        assertEquals(404, run(filter, "GET", "/admin/servers/a", TOOLS, null).status());
        assertEquals(404, run(filter, "POST", "/admin/tools", TOOLS, null).status());
    }

    @Test
    void withoutAdminPortNothingMutatingIsServed() throws Exception {
        AdminAccessFilter none = new AdminAccessFilter(0, TOKEN);
        assertEquals(404, run(none, "POST", "/admin/servers", TOOLS, "Bearer " + TOKEN).status());
        assertTrue(run(none, "GET", "/admin/tools", TOOLS, null).passed());
    }

    @Test
    void openReadMatchesOnlyTheToolLists() {
        assertTrue(AdminAccessFilter.isOpenRead("GET", "/admin/servers/abc/tools"));
        assertFalse(AdminAccessFilter.isOpenRead("GET", "/admin/servers/abc/tools/extra"));
        assertFalse(AdminAccessFilter.isOpenRead("HEAD", "/admin/tools"));
    }
}
