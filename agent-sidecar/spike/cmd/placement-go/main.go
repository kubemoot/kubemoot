// Command placement-go is the spike's Go prototype of the placement service API. It implements only the
// first rung of AlgorithmPlacement (a warm server with a free slot), with the design a single-writer
// service allows: server state kept current by a KV watch, tickets counted in memory, and the KV ticket
// entries written behind as a mirror for other readers. It is not a port of the policy.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
)

type serverState struct {
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	MaxParallel  int      `json:"maxParallel"`
	LoadedModels []string `json:"loadedModels"`
	Ready        bool     `json:"ready"`
}

type service struct {
	tickets nats.KeyValue
	mu      sync.Mutex
	states  map[string]serverState // kept current by the watch
	active  map[string]int         // server -> tickets held, authoritative in this process
	open    map[string]string      // ticket id -> server
	mirror  chan func()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "listen address")
	natsURL := flag.String("nats", "nats://127.0.0.1:4222", "NATS URL")
	static := flag.String("static", "", "answer every call with this endpoint and no state work (measures the hop alone)")
	flag.Parse()
	if *static != "" {
		serveStatic(*addr, *static)
		return
	}
	nc, err := nats.Connect(*natsURL)
	if err != nil {
		log.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		log.Fatal(err)
	}
	s := &service{states: map[string]serverState{}, active: map[string]int{}, open: map[string]string{}, mirror: make(chan func(), 4096)}
	if s.tickets, err = js.KeyValue(kbus.ServerTicketsBucket); err != nil {
		log.Fatal(err)
	}
	states, err := js.KeyValue("kubemoot_server_state")
	if err != nil {
		log.Fatal(err)
	}
	s.watch(states)
	go func() {
		for f := range s.mirror {
			f()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /placement/v1/place", s.place)
	mux.HandleFunc("POST /placement/v1/release", s.release)
	log.Fatal((&http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

// watch keeps the server states current from the KV bucket, so a placement reads memory, not NATS.
func (s *service) watch(kv nats.KeyValue) {
	w, err := kv.WatchAll()
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for e := range w.Updates() {
			if e == nil {
				continue
			}
			s.apply(e)
		}
	}()
}

func (s *service) apply(e nats.KeyValueEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Operation() == nats.KeyValueDelete || e.Operation() == nats.KeyValuePurge {
		delete(s.states, e.Key())
		return
	}
	var st serverState
	if json.Unmarshal(e.Value(), &st) == nil {
		s.states[e.Key()] = st
	}
}

func (s *service) place(w http.ResponseWriter, r *http.Request) {
	var req placement.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply, ok := s.pick(req)
	if !ok {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "kubemoot_no_fit", "reason": "no server has room now"})
		return
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func (s *service) pick(req placement.Request) (placement.Reply, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, model := range req.Models {
		for _, st := range s.states {
			if !st.Ready || !contains(st.LoadedModels, model) || s.active[st.Name] >= st.MaxParallel {
				continue
			}
			id := uuid.NewString()
			s.active[st.Name]++
			s.open[id] = st.Name
			s.writeBehind(st.Name, id, model)
			return placement.Reply{Server: st.Name, Endpoint: st.Endpoint, Model: model, Reason: "warm, free slot",
				Ticket: id, Evicted: []string{}}, true
		}
	}
	return placement.Reply{}, false
}

// writeBehind mirrors the ticket into the KV bucket for other readers; the in-memory count is the authority.
func (s *service) writeBehind(server, id, model string) {
	key := kbus.KeyToken(server) + "__" + id
	body, _ := json.Marshal(map[string]any{"ticketId": id, "server": server, "holderId": "placement-go",
		"claimedAt": time.Now().UTC().Format(time.RFC3339), "modelName": model})
	select {
	case s.mirror <- func() { _, _ = s.tickets.Create(key, body) }:
	default:
	}
}

func (s *service) release(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	server, ok := s.open[req.Ticket]
	if ok {
		delete(s.open, req.Ticket)
		s.active[server]--
		key := kbus.KeyToken(server) + "__" + req.Ticket
		select {
		case s.mirror <- func() { _ = s.tickets.Delete(key) }:
		default:
		}
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// serveStatic answers every placement with one fixed server: the service hop without any state work.
func serveStatic(addr, endpoint string) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /placement/v1/place", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(placement.Reply{Server: "static", Endpoint: endpoint, Model: "qwen3:14b",
			Reason: "static", Ticket: "t", Evicted: []string{}})
	})
	mux.HandleFunc("POST /placement/v1/release", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	log.Fatal((&http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
