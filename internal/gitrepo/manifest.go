// Package gitrepo proposes changes in a GitOps repository: it finds the manifest of a
// workload, edits its container requests, and opens a pull request on GitHub, GitLab or
// Gitea through their REST APIs.
package gitrepo

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Change is a container's new requests; an empty one leaves that request alone.
type Change struct {
	CPU    string
	Memory string
}

// EditRequests sets the requests of a workload's containers in a YAML file of one or more
// documents. It returns false when the file holds no such workload: kind and name, and
// the namespace when the manifest says one. Comments and the order of keys are kept.
func EditRequests(content []byte, kind, namespace, name string, changes map[string]Change) ([]byte, bool, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var docs []*yaml.Node
	found := false
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, false, err
		}
		if !found && matches(&doc, kind, namespace, name) {
			found = editContainers(&doc, changes)
		}
		docs = append(docs, &doc)
	}
	if !found {
		return content, false, nil
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, false, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return out.Bytes(), true, nil
}

// get returns the value under key in a mapping node.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ensure returns the mapping under key, adding it when missing and turning an empty
// value (resources: with nothing under it) into one.
func ensure(m *yaml.Node, key string) *yaml.Node {
	if v := get(m, key); v != nil {
		if v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == "") {
			v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.MappingNode, "!!map", "", 0, nil
		}
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v
}

// set writes a scalar under key.
func set(m *yaml.Node, key, value string) {
	if v := get(m, key); v != nil {
		v.Kind, v.Value, v.Tag, v.Style = yaml.ScalarNode, value, "!!str", 0
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func root(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

func matches(doc *yaml.Node, kind, namespace, name string) bool {
	r := root(doc)
	meta := get(r, "metadata")
	if scalar(get(r, "kind")) != kind || scalar(get(meta, "name")) != name {
		return false
	}
	ns := scalar(get(meta, "namespace"))
	return ns == "" || ns == namespace
}

func editContainers(doc *yaml.Node, changes map[string]Change) bool {
	containers := get(get(get(get(root(doc), "spec"), "template"), "spec"), "containers")
	if containers == nil || containers.Kind != yaml.SequenceNode {
		return false
	}
	edited := false
	for _, c := range containers.Content {
		ch, ok := changes[scalar(get(c, "name"))]
		if !ok {
			continue
		}
		requests := ensure(ensure(c, "resources"), "requests")
		if strings.TrimSpace(ch.CPU) != "" {
			set(requests, "cpu", ch.CPU)
			edited = true
		}
		if strings.TrimSpace(ch.Memory) != "" {
			set(requests, "memory", ch.Memory)
			edited = true
		}
	}
	return edited
}
