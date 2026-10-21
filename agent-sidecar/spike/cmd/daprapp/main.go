// Command daprapp is a throwaway Dapr application for the spike: it declares a programmatic subscription
// and prints every delivery daprd makes, so the test can see what pubsub.jetstream hands an app.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9300", "listen address")
	topic := flag.String("topic", "kubemoot.discuss.spike.crew.>", "topic (NATS subject) to subscribe to")
	raw := flag.Bool("raw", true, "ask for raw payloads (no CloudEvents envelope)")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dapr/subscribe", func(w http.ResponseWriter, _ *http.Request) {
		meta := map[string]string{}
		if *raw {
			meta["rawPayload"] = "true"
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"pubsubname": "natspubsub", "topic": *topic, "route": "/events", "metadata": meta}})
	})
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Printf("DELIVERED %s content-type=%s body=%s\n", time.Now().Format("15:04:05.000"), r.Header.Get("Content-Type"), b)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	log.Fatal((&http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}
