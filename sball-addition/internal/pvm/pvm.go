// Package pvm is a minimal client for the PVM/sballd XML-RPC socket.
//
// Wire format (identical to the pvm-wrapper/pvm-assistant client):
//
//	request  := <15 digit zero padded length><xml>
//	response := <15 digit zero padded length><xml>
//
// The request XML is:
//
//	<request><token>..</token><module_id>..</module_id>
//	  <command_id>..</command_id><parameter>..</parameter></request>
//
// Commands used here, as implemented by sballd:
//
//	module 0  command 6   "show pvm status"  -> cluster node list
//	module 0  command 15  "sballMaster()"    -> local cluster master node id
//	module 3  command 0   "login"            -> session token
//	module 3  command 9   "logout"
package pvm

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// RequestLength is the decimal width of the length prefix.
const RequestLength = 15

// Module and command ids.
const (
	modulePVM  = 0
	moduleAuth = 3
	cmdShowPVM = 6
	cmdMaster  = 15
	cmdLogin   = 0
	cmdLogout  = 9
)

// ErrUnavailable is returned when the PVM socket cannot be reached, which is
// the normal situation while sballd is still starting.
var ErrUnavailable = errors.New("pvm socket unavailable")

// Node is one cluster member as reported by "show pvm status".
type Node struct {
	ID        string
	Name      string
	NodeState string // up | down | ...
	PVMState  string // quorate | ...
	IP        string
	Port      string
}

// IsUp reports whether the node's own daemon is running.
func (n Node) IsUp() bool { return strings.EqualFold(n.NodeState, "up") }

// IsQuorate reports whether the node reports the quorate state.
func (n Node) IsQuorate() bool { return strings.EqualFold(n.PVMState, "quorate") }

// Client talks to one PVM socket.
type Client struct {
	Host    string
	Port    int
	Token   string
	Timeout time.Duration
}

// New returns a client for host:port with a 5 second timeout.
func New(host string, port int) *Client {
	return &Client{Host: host, Port: port, Timeout: 5 * time.Second}
}

// Addr returns the dial target.
func (c *Client) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 5 * time.Second
	}
	return c.Timeout
}

// Ping reports whether the socket accepts a connection.
func (c *Client) Ping() error {
	conn, err := net.DialTimeout("tcp", c.Addr(), c.timeout())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return conn.Close()
}

// Request sends one framed command and returns the raw response payload with
// the length prefix removed.
func (c *Client) Request(moduleID, commandID int, params string) (string, error) {
	cmd := "<request><token>" + c.Token + "</token>" +
		"<module_id>" + strconv.Itoa(moduleID) + "</module_id>" +
		"<command_id>" + strconv.Itoa(commandID) + "</command_id>" +
		"<parameter>" + params + "</parameter></request>"
	return c.raw(cmd)
}

// RequestByName is the name based variant of Request.
func (c *Client) RequestByName(moduleName, commandName, params string) (string, error) {
	cmd := "<request><token>" + c.Token + "</token>" +
		"<module_name>" + moduleName + "</module_name>" +
		"<command_name>" + commandName + "</command_name>" +
		"<parameter>" + params + "</parameter></request>"
	return c.raw(cmd)
}

func (c *Client) raw(cmd string) (string, error) {
	conn, err := net.DialTimeout("tcp", c.Addr(), c.timeout())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.timeout())
	_ = conn.SetDeadline(deadline)

	framed := fmt.Sprintf("%0*d%s", RequestLength, len(cmd), cmd)
	if _, err := conn.Write([]byte(framed)); err != nil {
		return "", fmt.Errorf("%w: write: %v", ErrUnavailable, err)
	}

	var sb strings.Builder
	buf := make([]byte, 8192)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				// Some daemon builds keep the connection open. If a complete
				// looking payload already arrived, use it instead of failing.
				if sb.Len() > 0 {
					break
				}
				return "", fmt.Errorf("%w: read timed out after %s", ErrUnavailable, c.timeout())
			}
			// io.EOF or a reset after a full response is normal.
			break
		}
	}

	payload := sb.String()
	if len(payload) <= RequestLength {
		return "", fmt.Errorf("%w: short response %q", ErrUnavailable, truncate(payload, 120))
	}
	return payload[RequestLength:], nil
}

// Login authenticates and stores the session token.
func (c *Client) Login(username, password string) error {
	params := "<params><username>" + xmlEscape(username) + "</username>" +
		"<password>" + xmlEscape(password) + "</password></params>"
	resp, err := c.Request(moduleAuth, cmdLogin, params)
	if err != nil {
		return err
	}
	if status := responseStatus(resp); status == "failed" {
		return fmt.Errorf("pvm login failed: %s", responseWhat(resp))
	}
	token := elementText(resp, "description")
	if token == "" {
		return fmt.Errorf("pvm login returned no token")
	}
	c.Token = token
	return nil
}

// Logout drops the session token.
func (c *Client) Logout() {
	if c.Token == "" {
		return
	}
	_, _ = c.Request(moduleAuth, cmdLogout, "")
	c.Token = ""
}

// MasterNodeID returns the cluster master node id ("1").
func (c *Client) MasterNodeID() (string, error) {
	resp, err := c.Request(modulePVM, cmdMaster, "")
	if err != nil {
		return "", err
	}
	if responseStatus(resp) == "failed" {
		return "", fmt.Errorf("sballMaster() failed: %s", responseWhat(resp))
	}
	id := strings.TrimSpace(elementText(resp, "master_node"))
	if id == "" {
		return "", fmt.Errorf("sballMaster() returned no master_node")
	}
	if id == "0" {
		return "", fmt.Errorf("master node id is 0 (cluster has no master)")
	}
	return id, nil
}

// Nodes returns the cluster node table from "show pvm status".
func (c *Client) Nodes() ([]Node, error) {
	resp, err := c.Request(modulePVM, cmdShowPVM, "")
	if err != nil {
		return nil, err
	}
	if responseStatus(resp) == "failed" {
		return nil, fmt.Errorf("show pvm status failed: %s", responseWhat(resp))
	}
	desc := innerElement(resp, "description")
	if strings.TrimSpace(desc) == "" {
		desc = resp
	}
	nodes, err := ParseNodes(desc)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("show pvm status returned no nodes")
	}
	return nodes, nil
}

// Status is a single consistent snapshot of the local node's cluster state.
type Status struct {
	MasterID     string
	Nodes        []Node
	Local        *Node
	LocalName    string
	Quorate      bool
	State        string // local PVM state, or "down"/"unknown"
	MasterKnown  bool
	IsMaster     bool
	LocalQueried bool
}

// Status queries master id and the node table and resolves the local node.
func (c *Client) Status() (*Status, error) {
	st := &Status{}

	if id, err := c.MasterNodeID(); err != nil {
		return st, err
	} else {
		st.MasterID = id
		st.MasterKnown = true
	}

	nodes, err := c.Nodes()
	if err != nil {
		return st, err
	}
	st.Nodes = nodes

	local := LocalNode(nodes)
	if local != nil {
		st.Local = local
		st.LocalName = local.Name
		st.Quorate = local.IsUp() && local.IsQuorate()
		st.State = local.PVMState
		if strings.TrimSpace(local.ID) == st.MasterID {
			st.IsMaster = true
		}
		st.LocalQueried = true
	} else {
		st.State = "unknown"
	}
	return st, nil
}

// LocalName returns the local short hostname (without domain).
func LocalName() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return ShortName(h)
}

// ShortName strips the domain part of a hostname.
func ShortName(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.Index(h, "."); i > 0 {
		return h[:i]
	}
	return h
}

// LocalNode picks the node entry that describes this host. It matches the
// short hostname first and falls back to comparing IP addresses with the local
// interface addresses.
func LocalNode(nodes []Node) *Node {
	local := strings.ToLower(LocalName())
	if local != "" {
		for i := range nodes {
			if strings.ToLower(ShortName(nodes[i].Name)) == local || strings.ToLower(nodes[i].Name) == local {
				return &nodes[i]
			}
		}
	}

	localIPs := localAddresses()
	if len(localIPs) > 0 {
		for i := range nodes {
			ip := strings.TrimSpace(nodes[i].IP)
			if pos := strings.Index(ip, "/"); pos > 0 {
				ip = ip[:pos]
			}
			if _, ok := localIPs[ip]; ok {
				return &nodes[i]
			}
		}
	}
	return nil
}

func localAddresses() map[string]struct{} {
	out := map[string]struct{}{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		out[ip.String()] = struct{}{}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
