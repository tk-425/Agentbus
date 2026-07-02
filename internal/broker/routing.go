package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tk-425/agentbus/internal/message"
)

// Route delivers msg to its resolved recipient: enqueued locally when the
// target Agent instance belongs to this broker's project, otherwise forwarded
// to the owning broker found through the shared registry. A target in neither
// the local nor the shared registry is an error — never silently queued.
// Replies take the same path: a Reply whose requester lives in another project
// resolves through the shared registry and is forwarded to that broker.
func (b *Broker) Route(msg message.Message) error {
	localProject := b.Registry.LocalProject()
	inst, err := b.Registry.Resolve(msg.To, localProject)
	if err != nil {
		return err
	}
	// The queue is keyed by bare instance name; strip any @project addressing.
	msg.To = inst.Name
	if inst.Project == localProject {
		return b.Send(msg)
	}
	return forward(inst.BrokerPort, msg)
}

// forward hands msg over to the peer broker listening on the loopback port
// recorded in the shared registry. The peer's /forward endpoint enqueues
// locally — it never re-routes, so a forwarded Message cannot loop.
func forward(port int, msg message.Message) error {
	body, _ := json.Marshal(msg)
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/forward", port), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("forward to broker on port %d: %w", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("forward to broker on port %d: %s", port, resp.Status)
	}
	return nil
}
