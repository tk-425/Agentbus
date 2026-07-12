// Package registry tracks registered Agent instances. Task 1 is a single-project,
// in-memory map keyed by instance name; per-project scoping, pane-derived
// identity, and SQLite persistence arrive in Tasks 3 and 5.
package registry

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrUnknownAgent reports a send target registered in neither the local nor the
// shared registry. Callers must surface it loudly — an unknown Agent instance is
// never silently queued.
var ErrUnknownAgent = errors.New("unknown agent")

// Instance is a registered Agent instance: a specific pane bound to a name.
// BrokerPort is the port of the broker that registered it — zero for a purely
// in-memory instance, populated on shared-registry lookups so routing can
// forward to the owning broker.
type Instance struct {
	Name       string
	Project    string
	PaneID     string
	Backend    string
	BrokerPort int
}

// instanceKey scopes an instance name to its project so names are unique per
// project, not globally.
type instanceKey struct {
	project string
	name    string
}

// Registry is an in-memory map of (project, name) -> Instance, optionally
// backed by the shared SQLite agents table for cross-broker lookup.
type Registry struct {
	mu        sync.Mutex
	instances map[instanceKey]Instance
	byPane    map[string]instanceKey // paneID -> the instance bound to it
	counters  map[string]int         // (project,type) -> highest suffix issued

	db           *sql.DB // shared agents store; nil for the in-memory fast path
	brokerPort   int     // this broker's port, recorded in write-through rows
	localProject string  // project label this broker serves; Resolve tries it first
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{
		instances: map[instanceKey]Instance{},
		byPane:    map[string]instanceKey{},
		counters:  map[string]int{},
	}
}

// AttachDB backs the registry with the shared agents table so register and
// unregister write through and LookupShared can resolve agents across brokers.
// brokerPort is recorded on each written row. Passing a nil db is a no-op.
func (r *Registry) AttachDB(db *sql.DB, brokerPort int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
	r.brokerPort = brokerPort
}

// SetLocalProject records the project label this broker serves. Resolve treats
// it as the local project a bare target name is tried against first.
func (r *Registry) SetLocalProject(project string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.localProject = project
}

// LocalProject returns the project label set by SetLocalProject.
func (r *Registry) LocalProject() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.localProject
}

// Resolve turns a send target into a registered Agent instance. A bare name
// resolves against localProject first, then falls back to the shared registry;
// a name@project target resolves locally when project is localProject and
// through the shared registry otherwise. The project label is the project-root
// basename (finding F1). A target in neither registry returns ErrUnknownAgent.
func (r *Registry) Resolve(target, localProject string) (Instance, error) {
	name, project, addressed := strings.Cut(target, "@")
	if !addressed || project == localProject {
		r.mu.Lock()
		inst, ok := r.instances[instanceKey{localProject, name}]
		r.mu.Unlock()
		if ok {
			return inst, nil
		}
	}
	if addressed {
		if inst, ok := r.LookupShared(project, name); ok {
			return inst, nil
		}
	} else if inst, ok := r.lookupSharedByName(name); ok {
		return inst, nil
	}
	return Instance{}, fmt.Errorf("%w: %q", ErrUnknownAgent, target)
}

// RegisterType registers a bare agent type into a project and returns the
// auto-suffixed Agent instance name (claude -> claude-1, then claude-2). The
// suffix counter is monotonic per (project, type), so a name is never reused
// within a session.
func (r *Registry) RegisterType(project, agentType, paneID string, backend ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ckey := project + "\x00" + agentType
	r.counters[ckey]++
	name := fmt.Sprintf("%s-%d", agentType, r.counters[ckey])
	key := instanceKey{project, name}
	inst := Instance{Name: name, Project: project, PaneID: paneID}
	if len(backend) > 0 {
		inst.Backend = backend[0]
	}
	r.instances[key] = inst
	r.byPane[paneID] = key
	r.writeThrough(inst)
	return name, nil
}

// Register stores an instance under its (Project, Name).
func (r *Registry) Register(inst Instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := instanceKey{inst.Project, inst.Name}
	r.instances[key] = inst
	r.byPane[inst.PaneID] = key
	r.writeThrough(inst)
}

// Unregister removes an Agent instance from a project. The suffix counter is
// not decremented, so the freed name is never reissued within the session.
func (r *Registry) Unregister(project, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := instanceKey{project, name}
	if inst, ok := r.instances[key]; ok {
		delete(r.byPane, inst.PaneID)
	}
	delete(r.instances, key)
	if r.db != nil {
		r.db.Exec(`DELETE FROM agents WHERE project = ? AND name = ?`, project, name)
	}
}

// writeThrough upserts inst into the shared agents table when a DB is attached.
// Called with r.mu held. Errors are intentionally swallowed: the in-memory map
// is authoritative for the local broker, and the shared store is a best-effort
// resolution surface for cross-broker lookup.
func (r *Registry) writeThrough(inst Instance) {
	if r.db == nil {
		return
	}
	r.db.Exec(`
		INSERT INTO agents (project, name, broker_port, pane_id, backend, registered_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, name) DO UPDATE SET
			broker_port   = excluded.broker_port,
			pane_id       = excluded.pane_id,
			backend       = excluded.backend,
			registered_at = excluded.registered_at`,
		inst.Project, inst.Name, r.brokerPort, inst.PaneID, inst.Backend,
		time.Now().UTC().Format(time.RFC3339))
}

// LookupShared resolves an Agent instance by (project, name) through the shared
// agents table, finding agents registered by any broker. Returns false when no
// DB is attached or no such row exists.
func (r *Registry) LookupShared(project, name string) (Instance, bool) {
	r.mu.Lock()
	db := r.db
	r.mu.Unlock()
	if db == nil {
		return Instance{}, false
	}
	var inst Instance
	err := db.QueryRow(
		`SELECT project, name, broker_port, pane_id, backend FROM agents WHERE project = ? AND name = ?`,
		project, name,
	).Scan(&inst.Project, &inst.Name, &inst.BrokerPort, &inst.PaneID, &inst.Backend)
	if err != nil {
		return Instance{}, false
	}
	return inst, true
}

// ListShared returns all registered Agent instances from the shared registry,
// ordered deterministically by project then name.
func (r *Registry) ListShared() ([]Instance, error) {
	r.mu.Lock()
	db := r.db
	r.mu.Unlock()
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT project, name, broker_port, pane_id, backend
		FROM agents
		ORDER BY project, name
	`)
	if err != nil {
		return nil, fmt.Errorf("list shared agents: %w", err)
	}
	defer rows.Close()

	var out []Instance
	for rows.Next() {
		var inst Instance
		if err := rows.Scan(&inst.Project, &inst.Name, &inst.BrokerPort, &inst.PaneID, &inst.Backend); err != nil {
			return nil, fmt.Errorf("scan shared agent: %w", err)
		}
		out = append(out, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shared agents: %w", err)
	}
	return out, nil
}

// ResolveUnregisterTarget resolves an exact Agent instance removal target. A
// bare name means the local project; name@project targets that project
// explicitly. Unknown targets return ErrUnknownAgent.
func (r *Registry) ResolveUnregisterTarget(localProject, target string) (Instance, error) {
	name, project, addressed := strings.Cut(target, "@")
	if !addressed {
		project = localProject
	}
	inst, ok := r.LookupShared(project, name)
	if !ok {
		return Instance{}, fmt.Errorf("%w: %q", ErrUnknownAgent, target)
	}
	return inst, nil
}

// lookupSharedByName resolves a bare Agent instance name through the shared
// agents table when it is absent locally. Ordered by project for determinism
// when the same instance name exists in several projects.
func (r *Registry) lookupSharedByName(name string) (Instance, bool) {
	r.mu.Lock()
	db := r.db
	r.mu.Unlock()
	if db == nil {
		return Instance{}, false
	}
	var inst Instance
	err := db.QueryRow(
		`SELECT project, name, broker_port, pane_id, backend FROM agents WHERE name = ? ORDER BY project LIMIT 1`,
		name,
	).Scan(&inst.Project, &inst.Name, &inst.BrokerPort, &inst.PaneID, &inst.Backend)
	if err != nil {
		return Instance{}, false
	}
	return inst, true
}

// LookupSharedByPane resolves the Agent instance bound to paneID through the
// shared agents table. Out-of-process CLI commands (e.g. `whoami`) run with an
// empty in-memory registry, so pane identity must come from the DB, not the
// byPane cache ResolveByPane reads.
func (r *Registry) LookupSharedByPane(paneID string) (Instance, bool) {
	r.mu.Lock()
	db := r.db
	r.mu.Unlock()
	if db == nil || paneID == "" {
		return Instance{}, false
	}
	var inst Instance
	err := db.QueryRow(
		`SELECT project, name, broker_port, pane_id, backend FROM agents WHERE pane_id = ? ORDER BY project LIMIT 1`,
		paneID,
	).Scan(&inst.Project, &inst.Name, &inst.BrokerPort, &inst.PaneID, &inst.Backend)
	if err != nil {
		return Instance{}, false
	}
	return inst, true
}

// ResolveByPane returns the Agent instance bound to paneID, deriving identity
// from the pane a command runs in.
func (r *Registry) ResolveByPane(paneID string) (Instance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.byPane[paneID]
	if !ok {
		return Instance{}, false
	}
	inst, ok := r.instances[key]
	return inst, ok
}

// Lookup returns the first instance registered under name across all projects.
// Sufficient for the single-project paths; cross-project resolution is Task 7.
func (r *Registry) Lookup(name string) (Instance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, inst := range r.instances {
		if k.name == name {
			return inst, true
		}
	}
	return Instance{}, false
}

// All returns every registered instance.
func (r *Registry) All() []Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Instance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, inst)
	}
	return out
}
