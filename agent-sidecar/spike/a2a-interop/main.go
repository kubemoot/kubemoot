// Command a2a-interop checks the sidecar's hand-written A2A client against the official Go SDK's server
// (a2asrv, REST handler): it serves a harness with the SDK and sends one turn with the sidecar's client.
package main

import (
	"context"
	"fmt"
	"iter"
	"log"
	"net"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	sidecar "github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
)

type executor struct{}

func (*executor) Execute(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		var question string
		if ec.Message != nil {
			for _, p := range ec.Message.Parts {
				question += p.Text()
			}
		}
		yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("sdk server heard: "+question)), nil)
	}
}

func (*executor) Cancel(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(func(a2a.Event, error) bool) {}
}

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	root := "http://" + ln.Addr().String()
	card := &a2a.AgentCard{
		Name:                "sdk-harness",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(root, a2a.TransportProtocolHTTPJSON)},
		DefaultInputModes:   []string{"text"},
		DefaultOutputModes:  []string{"text"},
	}
	mux := http.NewServeMux()
	mux.Handle("/", a2asrv.NewRESTHandler(a2asrv.NewHandler(&executor{})))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("sdk server saw: %s %s content-type=%s\n", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		mux.ServeHTTP(w, r)
	})
	go func() { log.Fatal(http.Serve(ln, logged)) }()

	client, found, err := sidecar.Discover(context.Background(), root)
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	fmt.Printf("card: name=%q interfaces=%+v\n", found.Name, found.SupportedInterfaces)
	msg, _ := sidecar.NewTurn("m-1", "thread-1", "which nodes have a GPU?", sidecar.TurnInput{Kind: "discussion", Agent: "a", Crew: "c", Phase: "EVALUATING", TurnKey: "k"})
	resp, err := client.Send(context.Background(), msg)
	if err != nil {
		log.Fatalf("send: %v", err)
	}
	fmt.Printf("outcome: %+v\n", sidecar.Interpret(resp, nil))
}
