package hermes

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	memoryEnabledPath      = "memory.memory_enabled"
	userProfileEnabledPath = "memory.user_profile_enabled"
	memoryProviderPath     = "memory.provider"
	disabledMemoryPath     = "agent.disabled_toolsets[memory]"
)

type ownedYAMLValue struct {
	path    string
	existed bool
	value   *yaml.Node
}

type ConfigOwnership struct {
	values                   []ownedYAMLValue
	memoryMappingExisted     bool
	agentMappingExisted      bool
	disabledToolsetsExisted  bool
	disabledToolsetsOriginal *yaml.Node
	disabledMemoryWasPresent bool
}

func (ownership ConfigOwnership) Paths() []string {
	return []string{
		memoryEnabledPath,
		userProfileEnabledPath,
		memoryProviderPath,
		disabledMemoryPath,
	}
}

func MergeConfig(before []byte) ([]byte, ConfigOwnership, error) {
	document, err := parseYAMLDocument(before)
	if err != nil {
		return nil, ConfigOwnership{}, err
	}
	root := document.Content[0]
	_, memoryMappingExisted := mappingValue(root, "memory")
	_, agentMappingExisted := mappingValue(root, "agent")
	ownership := ConfigOwnership{
		memoryMappingExisted: memoryMappingExisted,
		agentMappingExisted:  agentMappingExisted,
	}
	for _, path := range []string{memoryEnabledPath, userProfileEnabledPath, memoryProviderPath} {
		node, exists, err := findYAMLPath(document.Content[0], strings.Split(path, "."))
		if err != nil {
			return nil, ConfigOwnership{}, err
		}
		ownership.values = append(ownership.values, ownedYAMLValue{path: path, existed: exists, value: cloneYAMLNode(node)})
	}

	disabled, exists, err := findYAMLPath(document.Content[0], []string{"agent", "disabled_toolsets"})
	if err != nil {
		return nil, ConfigOwnership{}, err
	}
	ownership.disabledToolsetsExisted = exists
	ownership.disabledToolsetsOriginal = cloneYAMLNode(disabled)
	if exists && disabled.Kind == yaml.SequenceNode {
		ownership.disabledMemoryWasPresent = sequenceContains(disabled, "memory")
	}

	if err := setYAMLValue(document.Content[0], strings.Split(memoryEnabledPath, "."), boolNode(false)); err != nil {
		return nil, ConfigOwnership{}, err
	}
	if err := setYAMLValue(document.Content[0], strings.Split(userProfileEnabledPath, "."), boolNode(false)); err != nil {
		return nil, ConfigOwnership{}, err
	}
	if err := setYAMLValue(document.Content[0], strings.Split(memoryProviderPath, "."), stringNode("mlink")); err != nil {
		return nil, ConfigOwnership{}, err
	}
	disabled, err = ensureSequencePath(document.Content[0], []string{"agent", "disabled_toolsets"})
	if err != nil {
		return nil, ConfigOwnership{}, err
	}
	if !sequenceContains(disabled, "memory") {
		disabled.Content = append(disabled.Content, stringNode("memory"))
	}

	after, err := yaml.Marshal(document)
	if err != nil {
		return nil, ConfigOwnership{}, fmt.Errorf("marshal Hermes config: %w", err)
	}
	return after, ownership, nil
}

func RestoreConfig(current []byte, ownership ConfigOwnership) ([]byte, error) {
	document, err := parseYAMLDocument(current)
	if err != nil {
		return nil, err
	}
	root := document.Content[0]
	wants := map[string]*yaml.Node{
		memoryEnabledPath:      boolNode(false),
		userProfileEnabledPath: boolNode(false),
		memoryProviderPath:     stringNode("mlink"),
	}
	for _, owned := range ownership.values {
		currentNode, exists, err := findYAMLPath(root, strings.Split(owned.path, "."))
		if err != nil {
			return nil, err
		}
		if !exists || !sameYAMLValue(currentNode, wants[owned.path]) {
			return nil, fmt.Errorf("managed Hermes config %q changed after install", owned.path)
		}
		if owned.existed {
			if err := setYAMLValue(root, strings.Split(owned.path, "."), cloneYAMLNode(owned.value)); err != nil {
				return nil, err
			}
		} else if err := removeYAMLPath(root, strings.Split(owned.path, ".")); err != nil {
			return nil, err
		}
	}

	if !ownership.disabledMemoryWasPresent {
		disabled, exists, err := findYAMLPath(root, []string{"agent", "disabled_toolsets"})
		if err != nil {
			return nil, err
		}
		if exists && disabled.Kind == yaml.SequenceNode {
			removeSequenceValue(disabled, "memory")
			if len(disabled.Content) == 0 {
				if ownership.disabledToolsetsExisted {
					if err := setYAMLValue(root, []string{"agent", "disabled_toolsets"}, cloneYAMLNode(ownership.disabledToolsetsOriginal)); err != nil {
						return nil, err
					}
				} else if err := removeYAMLPath(root, []string{"agent", "disabled_toolsets"}); err != nil {
					return nil, err
				}
			}
		} else if exists && !(disabled.Kind == yaml.ScalarNode && disabled.Tag == "!!null") {
			return nil, errors.New("managed Hermes disabled_toolsets changed after install")
		}
	}
	pruneCreatedEmptyMapping(root, "memory", ownership.memoryMappingExisted)
	pruneCreatedEmptyMapping(root, "agent", ownership.agentMappingExisted)

	restored, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal restored Hermes config: %w", err)
	}
	return restored, nil
}

func parseYAMLDocument(data []byte) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse Hermes config: %w", err)
	}
	if len(document.Content) == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("Hermes config root must be a mapping")
	}
	return &document, nil
}

func findYAMLPath(root *yaml.Node, parts []string) (*yaml.Node, bool, error) {
	current := root
	for _, part := range parts {
		if current.Kind != yaml.MappingNode {
			return nil, false, fmt.Errorf("Hermes config path %q crosses a non-mapping value", strings.Join(parts, "."))
		}
		next, exists := mappingValue(current, part)
		if !exists {
			return nil, false, nil
		}
		current = next
	}
	return current, true, nil
}

func setYAMLValue(root *yaml.Node, parts []string, value *yaml.Node) error {
	if len(parts) == 0 {
		return errors.New("empty Hermes config path")
	}
	parent, err := ensureMappingPath(root, parts[:len(parts)-1])
	if err != nil {
		return err
	}
	for index := 0; index+1 < len(parent.Content); index += 2 {
		if parent.Content[index].Value == parts[len(parts)-1] {
			parent.Content[index+1] = value
			return nil
		}
	}
	parent.Content = append(parent.Content, stringNode(parts[len(parts)-1]), value)
	return nil
}

func ensureMappingPath(root *yaml.Node, parts []string) (*yaml.Node, error) {
	current := root
	for _, part := range parts {
		if current.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("Hermes config path %q crosses a non-mapping value", strings.Join(parts, "."))
		}
		next, exists := mappingValue(current, part)
		if !exists {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			current.Content = append(current.Content, stringNode(part), next)
		} else if next.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("Hermes config path %q is not a mapping", part)
		}
		current = next
	}
	return current, nil
}

func ensureSequencePath(root *yaml.Node, parts []string) (*yaml.Node, error) {
	node, exists, err := findYAMLPath(root, parts)
	if err != nil {
		return nil, err
	}
	if !exists || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
		node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if err := setYAMLValue(root, parts, node); err != nil {
			return nil, err
		}
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("Hermes config %q must be a list", strings.Join(parts, "."))
	}
	return node, nil
}

func removeYAMLPath(root *yaml.Node, parts []string) error {
	if len(parts) == 0 {
		return errors.New("empty Hermes config path")
	}
	parent, exists, err := findYAMLPath(root, parts[:len(parts)-1])
	if err != nil || !exists {
		return err
	}
	if parent.Kind != yaml.MappingNode {
		return fmt.Errorf("Hermes config parent for %q is not a mapping", strings.Join(parts, "."))
	}
	for index := 0; index+1 < len(parent.Content); index += 2 {
		if parent.Content[index].Value == parts[len(parts)-1] {
			parent.Content = append(parent.Content[:index], parent.Content[index+2:]...)
			return nil
		}
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], true
		}
	}
	return nil, false
}

func pruneCreatedEmptyMapping(root *yaml.Node, key string, existed bool) {
	if existed {
		return
	}
	value, exists := mappingValue(root, key)
	if !exists || value.Kind != yaml.MappingNode || len(value.Content) != 0 {
		return
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			root.Content = append(root.Content[:index], root.Content[index+2:]...)
			return
		}
	}
}

func sequenceContains(sequence *yaml.Node, value string) bool {
	for _, item := range sequence.Content {
		if item.Kind == yaml.ScalarNode && item.Value == value {
			return true
		}
	}
	return false
}

func removeSequenceValue(sequence *yaml.Node, value string) {
	filtered := sequence.Content[:0]
	for _, item := range sequence.Content {
		if item.Kind == yaml.ScalarNode && item.Value == value {
			continue
		}
		filtered = append(filtered, item)
	}
	sequence.Content = filtered
}

func sameYAMLValue(left, right *yaml.Node) bool {
	if left == nil || right == nil {
		return left == right
	}
	return reflect.DeepEqual(normalizedYAMLValue(left), normalizedYAMLValue(right))
}

func normalizedYAMLValue(node *yaml.Node) any {
	var value any
	_ = node.Decode(&value)
	return value
}

func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	clone.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		clone.Content[index] = cloneYAMLNode(child)
	}
	return &clone
}

func boolNode(value bool) *yaml.Node {
	text := "false"
	if value {
		text = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text}
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
