package readops

import (
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
)

var errYAMLBudget = errors.New("the YAML expands past its value limit once aliases are followed")

// nodeValue converts a parsed YAML node into the values gojq accepts. Timestamps
// stay as their original text, mapping keys keep their original text, and each value
// visited (aliases included) spends from budget so an alias bomb stops early.
func nodeValue(n *yaml.Node, budget *int) (any, error) {
	if *budget--; *budget < 0 {
		return nil, errYAMLBudget
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return nodeValue(n.Content[0], budget)
	case yaml.AliasNode:
		return nodeValue(n.Alias, budget)
	case yaml.SequenceNode:
		return sequenceValue(n, budget)
	case yaml.MappingNode:
		return mappingValue(n, budget)
	}
	return scalarValue(n)
}

func sequenceValue(n *yaml.Node, budget *int) (any, error) {
	out := make([]any, 0, len(n.Content))
	for _, c := range n.Content {
		v, err := nodeValue(c, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func mappingValue(n *yaml.Node, budget *int) (any, error) {
	out := make(map[string]any, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, err := keyText(n.Content[i])
		if err != nil {
			return nil, err
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("the YAML mapping has two keys that read as %q", key)
		}
		v, err := nodeValue(n.Content[i+1], budget)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}

// keyText renders a mapping key as the text written in the file; a null key
// (empty or ~) reads as "null".
func keyText(k *yaml.Node) (string, error) {
	if k.Kind == yaml.AliasNode {
		k = k.Alias
	}
	if k.Kind != yaml.ScalarNode {
		return "", errors.New("the YAML has a mapping or list used as a key, which jq cannot address")
	}
	if k.ShortTag() == "!!null" {
		return "null", nil
	}
	return k.Value, nil
}

func scalarValue(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!str", "!!timestamp":
		return n.Value, nil
	case "!!int", "!!float":
		return numberValue(n.Value), nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
