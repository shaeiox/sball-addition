package pvm

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// xmlEscape escapes a value so it can be embedded in a request parameter.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// elementText returns the character data of the first element named name.
// It is deliberately tolerant: sballd wraps command payloads in
// <description> but the response envelope has varied between releases.
func elementText(doc, name string) string {
	dec := xml.NewDecoder(strings.NewReader(doc))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return ""
			}
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && strings.EqualFold(t.Name.Local, name) {
				depth = 1
				continue
			}
			if depth > 0 {
				depth++
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
				if depth == 0 {
					return "" // empty element
				}
			}
		case xml.CharData:
			if depth == 1 {
				s := strings.TrimSpace(string(t))
				if s != "" {
					// Return the first non-empty chunk: enough for tokens,
					// ids and status words.
					return s
				}
			}
		}
	}
}

// innerElement returns the raw inner content of the first element named name,
// preserving nested markup. This is how <description> payloads are read by the
// reference client (index based, not entity decoded).
func innerElement(doc, name string) string {
	open := "<" + name + ">"
	close := "</" + name + ">"
	i := strings.Index(doc, open)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	inner := rest[:j]
	// Some daemon builds entity-escape the payload.
	if !strings.Contains(inner, "<") && strings.Contains(inner, "&lt;") {
		inner = unescapeXML(inner)
	}
	return inner
}

func unescapeXML(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

// responseStatus extracts <status> from a response envelope.
func responseStatus(doc string) string {
	return strings.ToLower(elementText(doc, "status"))
}

// responseWhat extracts <what> (the error detail) from a failed response.
func responseWhat(doc string) string {
	if w := elementText(doc, "what"); w != "" {
		return w
	}
	return truncate(strings.TrimSpace(doc), 200)
}

// ParseNodes parses the node table out of a "show pvm status" payload.
//
// The reference implementation reads it as
//
//	<something><cluster><node>…</node></cluster></something>
//
// but the exact wrapper is not part of any contract, so parsing is done token
// by token: any element named node/host that carries the expected child
// elements becomes a Node.
func ParseNodes(doc string) ([]Node, error) {
	dec := xml.NewDecoder(strings.NewReader(doc))
	var (
		nodes []Node
		cur   *Node
		field string
		out   strings.Builder
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse pvm status xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := normalizeElement(t.Name.Local)
			if isNodeElement(name) {
				cur = &Node{}
				field = ""
				continue
			}
			if cur != nil {
				field = name
				out.Reset()
			}
		case xml.CharData:
			if cur != nil && field != "" {
				out.Write(t)
			}
		case xml.EndElement:
			name := normalizeElement(t.Name.Local)
			if isNodeElement(name) {
				if cur != nil {
					nodes = append(nodes, *cur)
					cur = nil
				}
				field = ""
				continue
			}
			if cur == nil {
				continue
			}
			value := strings.TrimSpace(out.String())
			out.Reset()
			switch name {
			case "id", "node_id", "nodeid":
				cur.ID = value
			case "name", "node_name", "nodename":
				cur.Name = value
			case "node_state", "nodestate":
				cur.NodeState = value
			case "sball_state", "sballstate", "pvm_state", "pvmstate":
				cur.PVMState = value
			case "ip", "address":
				cur.IP = value
			case "port":
				cur.Port = value
			}
			field = ""
		}
	}
	return nodes, nil
}

// normalizeElement lower-cases an element name and folds '-' into '_' so that
// every daemon spelling of node-state/sball-state is recognised.
func normalizeElement(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
}

func isNodeElement(name string) bool {
	switch name {
	case "node", "host", "node_info", "member":
		return true
	}
	return false
}
