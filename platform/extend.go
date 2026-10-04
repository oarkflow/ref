package platform

import (
	"fmt"
	"slices"
)

// IntentExtensionSpec changes an intent that is declared somewhere else: add
// nodes, replace a node, remove a node. It is how an application's flows are
// customised without editing the files that define them, so a deployment can
// add a validation rule, an audit log or a notification to a flow it does not
// own, and take it out again by deleting one file.
//
//	extend "audit_sends" {
//	  intent "sms.prepare"
//	  node "audit" { uses "log.event" kind pure requires [message] provides [audit] config { ... } }
//	  feed ["prepared"]       # the audit node runs before "prepared"
//	}
//
// Nodes are wired by the facts they require and provide, not by position, so
// "in between" means: require what comes before, and `feed` what comes after.
type IntentExtensionSpec struct {
	Name        string `bcl:",id"`
	Description string `bcl:"description"`
	// Intent is the intent being extended.
	Intent string `bcl:"intent"`
	// Nodes are added. A node whose name already exists is an error unless it
	// is listed in Replace.
	Nodes []NodeSpec `bcl:"node,block"`
	// Replace names existing nodes the extension's nodes of the same name
	// replace.
	Replace []string `bcl:"replace"`
	// Remove names existing nodes to delete. The facts they provided are also
	// taken out of other nodes' requires, so a node that only waited for the
	// removed one (a gate) carries on; a node that READS the removed fact fails
	// at load, because nothing provides it.
	Remove []string `bcl:"remove"`
	// Feed names existing nodes that must wait for the facts every added node
	// provides: the way to put an added node in front of an existing one.
	Feed []string `bcl:"feed"`
	// Response replaces the intent's response fact.
	Response string `bcl:"response"`
	// Disabled switches the extension off without deleting it.
	Disabled bool `bcl:"disabled"`
}

// expandExtensions applies every enabled extension, in the order the blocks
// were loaded (files load in name order), and returns the document with the
// extensions consumed.
func expandExtensions(doc Document) (Document, error) {
	if len(doc.Extensions) == 0 {
		return doc, nil
	}
	intents := make([]IntentSpec, len(doc.Intents))
	for i, in := range doc.Intents {
		in.Nodes = slices.Clone(in.Nodes)
		intents[i] = in
	}
	find := func(name string) int {
		for i := range intents {
			if intents[i].Name == name {
				return i
			}
		}
		return -1
	}
	seen := map[string]bool{}
	for _, ext := range doc.Extensions {
		if seen[ext.Name] {
			return doc, fmt.Errorf("extend %q is declared twice", ext.Name)
		}
		seen[ext.Name] = true
		if ext.Disabled {
			continue
		}
		at := find(ext.Intent)
		if at < 0 {
			return doc, fmt.Errorf("extend %q: there is no intent %q", ext.Name, ext.Intent)
		}
		in := &intents[at]
		index := func(node string) int {
			return slices.IndexFunc(in.Nodes, func(n NodeSpec) bool { return n.Name == node })
		}

		var removed []string
		for _, name := range ext.Remove {
			i := index(name)
			if i < 0 {
				return doc, fmt.Errorf("extend %q: intent %q has no node %q to remove", ext.Name, ext.Intent, name)
			}
			removed = append(removed, in.Nodes[i].Provides...)
			in.Nodes = slices.Delete(in.Nodes, i, i+1)
		}
		if len(removed) > 0 {
			for i := range in.Nodes {
				kept := make([]string, 0, len(in.Nodes[i].Requires))
				for _, fact := range in.Nodes[i].Requires {
					if !slices.Contains(removed, fact) {
						kept = append(kept, fact)
					}
				}
				in.Nodes[i].Requires = kept
			}
		}

		var added []string
		for _, node := range ext.Nodes {
			i := index(node.Name)
			switch {
			case i >= 0 && slices.Contains(ext.Replace, node.Name):
				in.Nodes[i] = node
			case i >= 0:
				return doc, fmt.Errorf("extend %q: intent %q already has a node %q (list it in replace to replace it)", ext.Name, ext.Intent, node.Name)
			default:
				in.Nodes = append(in.Nodes, node)
			}
			added = append(added, node.Provides...)
		}
		for _, name := range ext.Replace {
			if !slices.ContainsFunc(ext.Nodes, func(n NodeSpec) bool { return n.Name == name }) {
				return doc, fmt.Errorf("extend %q: replace %q, but the extension has no node of that name", ext.Name, name)
			}
		}
		for _, name := range ext.Feed {
			i := index(name)
			if i < 0 {
				return doc, fmt.Errorf("extend %q: intent %q has no node %q to feed", ext.Name, ext.Intent, name)
			}
			for _, fact := range added {
				if !slices.Contains(in.Nodes[i].Requires, fact) {
					in.Nodes[i].Requires = append(slices.Clone(in.Nodes[i].Requires), fact)
				}
			}
		}
		if ext.Response != "" {
			in.Response = ext.Response
		}
	}
	doc.Intents = intents
	doc.Extensions = nil
	return doc, nil
}
