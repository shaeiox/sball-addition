package pvm

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeServer speaks the framed protocol of sballd and answers with the payload
// produced by handle.
func fakeServer(t *testing.T, handle func(request string) string) (string, int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 15)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				n, err := strconv.Atoi(strings.TrimSpace(string(buf)))
				if err != nil || n <= 0 {
					return
				}
				req := make([]byte, n)
				if _, err := io.ReadFull(c, req); err != nil {
					return
				}
				resp := handle(string(req))
				framed := fmt.Sprintf("%015d%s", len(resp), resp)
				_, _ = c.Write([]byte(framed))
			}(conn)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	stop := func() { _ = ln.Close(); <-done }
	return "127.0.0.1", addr.Port, stop
}

const (
	masterResponse = `<status>success</status><description><master_node>2</master_node></description>`
)

func nodesResponse(name string, state string, pvmState string) string {
	return `<status>success</status><description><nodes>` +
		`<node><id>1</id><name>patrick</name><node-state>up</node-state><sball-state>quorate</sball-state><ip>172.16.101.106</ip><port>22</port></node>` +
		fmt.Sprintf(`<node><id>2</id><name>%s</name><node-state>%s</node-state><sball-state>%s</sball-state><ip>172.16.101.105</ip><port>22</port></node>`, name, state, pvmState) +
		`</nodes></description>`
}

func TestMasterNodeID(t *testing.T) {
	_, port, stop := fakeServer(t, func(req string) string {
		if !strings.Contains(req, "<module_id>0</module_id>") || !strings.Contains(req, "<command_id>15</command_id>") {
			t.Errorf("unexpected request: %s", req)
		}
		return masterResponse
	})
	defer stop()

	c := New("127.0.0.1", port)
	id, err := c.MasterNodeID()
	if err != nil {
		t.Fatalf("MasterNodeID: %v", err)
	}
	if id != "2" {
		t.Errorf("id = %q, want 2", id)
	}
}

func TestMasterNodeIDZeroIsAnError(t *testing.T) {
	_, port, stop := fakeServer(t, func(string) string {
		return `<status>success</status><description><master_node>0</master_node></description>`
	})
	defer stop()
	if _, err := New("127.0.0.1", port).MasterNodeID(); err == nil {
		t.Fatal("master id 0 must be reported as an error")
	}
}

func TestNodesParsing(t *testing.T) {
	local := LocalName()
	_, port, stop := fakeServer(t, func(string) string {
		return nodesResponse(local, "up", "quorate")
	})
	defer stop()

	nodes, err := New("127.0.0.1", port).Nodes()
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	if nodes[0].Name != "patrick" || nodes[0].IP != "172.16.101.106" || nodes[0].Port != "22" {
		t.Errorf("node[0] = %+v", nodes[0])
	}
	if !nodes[1].IsQuorate() || !nodes[1].IsUp() {
		t.Errorf("node[1] = %+v", nodes[1])
	}
}

func TestNodesParsingClusterWrapper(t *testing.T) {
	// The reference implementation reads ...<cluster><node>...</node></cluster>
	payload := `<status>success</status><description><cluster>` +
		`<node><id>1</id><name>a</name><node-state>up</node-state><sball-state>quorate</sball-state><ip>10.0.0.1</ip><port>22</port></node>` +
		`</cluster></description>`
	_, port, stop := fakeServer(t, func(string) string { return payload })
	defer stop()

	nodes, err := New("127.0.0.1", port).Nodes()
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "1" {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestNodesEscapedPayload(t *testing.T) {
	inner := `<nodes><node><id>1</id><name>a</name><node-state>up</node-state>` +
		`<sball-state>quorate</sball-state><ip>10.0.0.1</ip><port>22</port></node></nodes>`
	escaped := strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(inner)
	_, port, stop := fakeServer(t, func(string) string {
		return "<status>success</status><description>" + escaped + "</description>"
	})
	defer stop()
	nodes, err := New("127.0.0.1", port).Nodes()
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || !nodes[0].IsQuorate() {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestStatusQuorateAndMaster(t *testing.T) {
	local := LocalName()
	_, port, stop := fakeServer(t, func(req string) string {
		if strings.Contains(req, "<command_id>15</command_id>") {
			return masterResponse
		}
		return nodesResponse(local, "up", "quorate")
	})
	defer stop()

	c := New("127.0.0.1", port)
	st, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Quorate {
		t.Errorf("quorate = false, state = %q", st.State)
	}
	if st.MasterID != "2" {
		t.Errorf("master = %q", st.MasterID)
	}
	if st.Local == nil || st.Local.Name != local {
		t.Fatalf("local node = %+v", st.Local)
	}
	if !st.IsMaster {
		t.Error("this node has id 2 and the master is 2, so IsMaster should be true")
	}
}

func TestStatusNotQuorate(t *testing.T) {
	local := LocalName()
	_, port, stop := fakeServer(t, func(req string) string {
		if strings.Contains(req, "<command_id>15</command_id>") {
			return masterResponse
		}
		return nodesResponse(local, "up", "not-quorate")
	})
	defer stop()

	st, err := New("127.0.0.1", port).Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Quorate {
		t.Error("node must not be quorate")
	}
	if st.State != "not-quorate" {
		t.Errorf("state = %q", st.State)
	}
}

func TestUnavailableSocket(t *testing.T) {
	// A port nobody listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	c := New("127.0.0.1", port)
	c.Timeout = 500 * time.Millisecond
	if _, err := c.Status(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if err := c.Ping(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ping err = %v, want ErrUnavailable", err)
	}
}

func TestFailedStatus(t *testing.T) {
	_, port, stop := fakeServer(t, func(string) string {
		return `<status>failed</status><what>authentication required</what>`
	})
	defer stop()
	_, err := New("127.0.0.1", port).MasterNodeID()
	if err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("err = %v, want the <what> detail", err)
	}
}

func TestLogin(t *testing.T) {
	_, port, stop := fakeServer(t, func(req string) string {
		if strings.Contains(req, "<module_id>3</module_id>") && strings.Contains(req, "<command_id>0</command_id>") {
			if !strings.Contains(req, "<username>admin</username>") {
				t.Errorf("credentials not sent: %s", req)
			}
			return `<status>success</status><description>tok-123</description>`
		}
		return masterResponse
	})
	defer stop()

	c := New("127.0.0.1", port)
	if err := c.Login("admin", "secret"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.Token != "tok-123" {
		t.Errorf("token = %q", c.Token)
	}
}

func TestLocalNodeFallsBackToIP(t *testing.T) {
	// No node matches the hostname; the loopback address must not be used, so
	// the result is nil rather than a wrong node.
	nodes := []Node{{ID: "9", Name: "not-this-host-xyz", NodeState: "up", PVMState: "quorate", IP: "192.0.2.77"}}
	if got := LocalNode(nodes); got != nil {
		t.Errorf("LocalNode = %+v, want nil", got)
	}
}

func TestShortName(t *testing.T) {
	if got := ShortName("spongebob.example.com"); got != "spongebob" {
		t.Errorf("got %q", got)
	}
	if got := ShortName("spongebob"); got != "spongebob" {
		t.Errorf("got %q", got)
	}
}
