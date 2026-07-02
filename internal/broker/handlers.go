package broker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/registry"
)

// Handler returns the broker's HTTP routes. Both Serve (out of process) and the
// tests mount this same handler. Every route delegates to the in-memory Broker,
// so the single truncation point (Send) and request/reply asymmetry are shared
// by the HTTP and in-process paths alike.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/register", b.handleRegister)
	mux.HandleFunc("/send", b.handleSend)
	mux.HandleFunc("/forward", b.handleForward)
	mux.HandleFunc("/inbox/", b.handleInbox)
	mux.HandleFunc("/ack/", b.handleAck)
	return mux
}

type registerRequest struct {
	Project   string `json:"project"`
	AgentType string `json:"agent_type"`
	PaneID    string `json:"pane_id"`
}

type registerResponse struct {
	Name string `json:"name"`
}

func (b *Broker) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	name, err := b.Register(req.Project, req.AgentType, req.PaneID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, registerResponse{Name: name})
}

// handleSend routes a Message to its resolved recipient — locally or forwarded
// to a peer broker. An unknown target is a loud 404, never a silent enqueue.
func (b *Broker) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var msg message.Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	if err := b.Route(msg); err != nil {
		if errors.Is(err, registry.ErrUnknownAgent) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleForward enqueues a Message handed over by a peer broker. It is the
// terminus of cross-broker routing: a plain local enqueue that never re-routes,
// so a forwarded Message cannot bounce between brokers.
func (b *Broker) handleForward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var msg message.Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	if err := b.Send(msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *Broker) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent := strings.TrimPrefix(r.URL.Path, "/inbox/")
	if agent == "" {
		http.Error(w, "missing agent", http.StatusBadRequest)
		return
	}
	writeJSON(w, b.Inbox(agent))
}

// handleAck acknowledges a delivered message. Inbox is drain-on-read, so the
// message is already gone by the time a client acks; this endpoint exists for
// the documented route surface and returns success idempotently. Durable
// read-tracking against the messages table is future work.
func (b *Broker) handleAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
