package ai.kubemoot.agent.signal;

/** The consent-3 kinds as the operator hands them to an agent, for the signal tests. */
public final class SignalTestKinds {

    /** KUBEMOOT_DISCUSS_SIGNALS for consent-3: every kind the runtime has logic for. */
    public static final String CONSENT_3 = """
            [{"name":"agree","meaning":"your finding stands","terminal":true,"addressed":false},
             {"name":"concern","meaning":"a finding is wrong or unverified","terminal":true,"addressed":false},
             {"name":"question","meaning":"ask the agent that made a finding to recheck it","terminal":false,"addressed":true},
             {"name":"stand_aside","meaning":"nothing to add","terminal":true,"addressed":false},
             {"name":"tool_gap","meaning":"a tool is missing","terminal":true,"addressed":false},
             {"name":"no_data","meaning":"the tools returned nothing usable","terminal":true,"addressed":false},
             {"name":"block","meaning":"a finding contradicts your results","terminal":true,"addressed":false}]""";

    private SignalTestKinds() {
    }

    /** The consent-3 kinds, parsed. */
    public static DeclaredSignals consent3() {
        return DeclaredSignals.parse(CONSENT_3);
    }
}
