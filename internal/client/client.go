// Package client is the agent-facing handle to a Broker. It has two
// interchangeable backends behind one method set: an in-process *broker.Broker
// (used by the Watcher and its tests) and an HTTP transport against a running
// broker's port file (used by out-of-process CLI commands). Callers such as the
// Watcher are unchanged regardless of backend.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/message"
)

// Client sends Messages to and drains inboxes from a Broker. Exactly one backend
// is set: broker (in-process) or baseURL (HTTP).
type Client struct {
	broker  *broker.Broker
	baseURL string
	httpc   *http.Client
}

// New returns a Client backed by an in-process Broker.
func New(b *broker.Broker) *Client {
	return &Client{broker: b}
}

// NewRemote returns a Client that talks to a broker over HTTP at baseURL
// (e.g. http://127.0.0.1:7373).
func NewRemote(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpc: http.DefaultClient}
}

// Dial returns an HTTP-backed Client by reading the broker's port from portFile.
func Dial(portFile string) (*Client, error) {
	raw, err := os.ReadFile(portFile)
	if err != nil {
		return nil, fmt.Errorf("read port file: %w", err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("parse port file: %w", err)
	}
	return NewRemote("http://127.0.0.1:" + strconv.Itoa(port)), nil
}

// Register binds a bare agent type to a pane and returns the instance name.
func (c *Client) Register(project, agentType, paneID string, backend ...string) (string, error) {
	if c.broker != nil {
		return c.broker.Register(project, agentType, paneID, backend...)
	}
	payload := map[string]string{
		"project": project, "agent_type": agentType, "pane_id": paneID,
	}
	if len(backend) > 0 {
		payload["backend"] = backend[0]
	}
	body, _ := json.Marshal(payload)
	resp, err := c.httpc.Post(c.baseURL+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register: %s", resp.Status)
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Name, nil
}

// Send delivers msg to the broker.
func (c *Client) Send(msg message.Message) error {
	if c.broker != nil {
		return c.broker.Send(msg)
	}
	body, _ := json.Marshal(msg)
	resp, err := c.httpc.Post(c.baseURL+"/send", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("send: %s", resp.Status)
	}
	return nil
}

// Reply produces the terminal Reply to the Request identified by id. The broker
// resolves the original Requester from the recorded correlation, so the caller
// supplies only the id and body — never a Request's --from/--to. A non-2xx HTTP
// status (e.g. an unknown id → 404) surfaces as an error so it fails loudly at
// the command.
func (c *Client) Reply(id, body string) error {
	if c.broker != nil {
		return c.broker.Reply(id, body)
	}
	payload, _ := json.Marshal(map[string]string{"id": id, "body": body})
	resp, err := c.httpc.Post(c.baseURL+"/reply", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("reply: %s", resp.Status)
	}
	return nil
}

// Inbox drains the inbox for agent. On an HTTP error it returns nil, preserving
// the in-process signature the Watcher depends on.
func (c *Client) Inbox(agent string) []message.Message {
	if c.broker != nil {
		return c.broker.Inbox(agent)
	}
	resp, err := c.httpc.Get(c.baseURL + "/inbox/" + agent)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var msgs []message.Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil
	}
	return msgs
}

// Requests drains only Request messages for agent. The watcher uses this path
// so terminal Replies remain available for a human inbox read.
func (c *Client) Requests(agent string) []message.Message {
	if c.broker != nil {
		return c.broker.Requests(agent)
	}
	var requests []message.Message
	for _, msg := range c.Inbox(agent) {
		if msg.Kind == message.KindRequest {
			requests = append(requests, msg)
		}
	}
	return requests
}

// UnnotifiedReplies returns Replies awaiting an arrival notification for
// agent, without draining them (ADR-0002). Watchers run inside the broker
// process, so this is in-process only; over HTTP it reports none.
func (c *Client) UnnotifiedReplies(agent string) []message.Message {
	if c.broker != nil {
		return c.broker.UnnotifiedReplies(agent)
	}
	return nil
}

// MarkNotified records that arrival notifications for these message IDs were
// injected. In-process only, like UnnotifiedReplies.
func (c *Client) MarkNotified(ids []string) {
	if c.broker != nil {
		c.broker.MarkNotified(ids)
	}
}

// Ack acknowledges a delivered message by id for agent.
func (c *Client) Ack(agent, id string) error {
	if c.broker != nil {
		return nil // in-process inbox is drain-on-read; nothing to ack
	}
	resp, err := c.httpc.Post(c.baseURL+"/ack/"+agent+"/"+id, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ack: %s", resp.Status)
	}
	return nil
}
