// Package pvmtest provides an in-process fake of the PVM/sballd XML-RPC socket.
// It exists purely for tests (unit tests of the watcher and CLI, and the
// integration suite) and is never linked into the sball-addition binary.
package pvmtest

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Node describes one cluster member served by the fake socket.
type Node struct {
	ID        string
	Name      string
	NodeState string
	PVMState  string
	IP        string
	Port      string
}

// Server is a fake sballd socket.
type Server struct {
	mu           sync.Mutex
	ln           net.Listener
	nodes        []Node
	masterID     string
	quorateAfter time.Duration
	started      time.Time
	requests     []string
	closed       bool
}

// New starts a fake server on a free loopback port.
func New() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln:       ln,
		masterID: "1",
		started:  time.Now(),
		nodes: []Node{
			{ID: "1", Name: "node1", NodeState: "up", PVMState: "quorate", IP: "127.0.0.1", Port: "22"},
		},
	}
	go s.serve()
	return s, nil
}

// Host and Port return the dial target.
func (s *Server) Host() string { return "127.0.0.1" }

// Port returns the listening port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Master sets the master node id reported by the fake socket.
func (s *Server) Master(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.masterID = id
}

// SetNodes replaces the cluster node table.
func (s *Server) SetNodes(nodes ...Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = nodes
}

// QuorateAfter makes the local node report "not-quorate" for d after every
// state change, which is how tests simulate a slow cluster join.
func (s *Server) QuorateAfter(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quorateAfter = d
	s.started = time.Now()
}

// ResetQuorateTimer restarts the delay configured with QuorateAfter.
func (s *Server) ResetQuorateTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = time.Now()
}

// Requests returns every request received so far (for assertions).
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requests))
	copy(out, s.requests)
	return out
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	head := make([]byte, 15)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(head)))
	if err != nil || n <= 0 {
		return
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	req := string(body)

	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	resp := s.respond(req)
	_, _ = conn.Write([]byte(fmt.Sprintf("%015d%s", len(resp), resp)))
}

func (s *Server) respond(req string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.Contains(req, "<module_id>3</module_id>"):
		return "<status>success</status><description>token-1</description>"
	case strings.Contains(req, "<command_id>15</command_id>"):
		return "<status>success</status><description><master_node>" + s.masterID + "</master_node></description>"
	default:
		var b strings.Builder
		b.WriteString("<status>success</status><description><nodes>")
		for _, n := range s.nodes {
			pvmState := n.PVMState
			if pvmState == "quorate" && s.quorateAfter > 0 && time.Since(s.started) < s.quorateAfter {
				pvmState = "not-quorate"
			}
			fmt.Fprintf(&b, "<node><id>%s</id><name>%s</name><node-state>%s</node-state>"+
				"<sball-state>%s</sball-state><ip>%s</ip><port>%s</port></node>",
				n.ID, n.Name, n.NodeState, pvmState, n.IP, n.Port)
		}
		b.WriteString("</nodes></description>")
		return b.String()
	}
}
