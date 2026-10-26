package compose

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Add adds a container to the compose file, will err if file does not exist
func Add(dest string, resource string, container *Container) error {
	return edit(dest, func(services *yaml.Node) error {
		var node yaml.Node
		err := node.Encode(container)
		if err != nil {
			return err
		}

		// The zdap-proxy image gets overwritten by dcr/docker compose builds unless the build node is cleared when
		// docker merges the compose files, which is what the '!reset' tag does.
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "image" {
				reset := []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "build"},
					{Kind: yaml.MappingNode, Tag: "!reset", Style: yaml.FlowStyle},
				}
				node.Content = append(node.Content[:i], append(reset, node.Content[i:]...)...)
				break
			}
		}

		for i := 0; i+1 < len(services.Content); i += 2 {
			if services.Content[i].Value == resource {
				services.Content[i+1] = &node
				return nil
			}
		}
		services.Content = append(services.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: resource}, &node)
		return nil
	})
}

func RemoveClone(dest string, resources []string) error {
	return edit(dest, func(services *yaml.Node) error {
		kept := services.Content[:0]
		for i := 0; i+1 < len(services.Content); i += 2 {
			if slices.Contains(resources, services.Content[i].Value) {
				continue
			}
			kept = append(kept, services.Content[i], services.Content[i+1])
		}
		services.Content = kept
		return nil
	})
}

// edit works on the YAML node tree rather than DockerCompose, so that comments and keys zdap doesn't know about survive.
func edit(dest string, fn func(services *yaml.Node) error) error {
	// Get exclusive lock on file first to prevent race conditions
	f, err := os.OpenFile(dest, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Use flock to get an exclusive lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("failed to acquire exclusive lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // Release lock when done

	// Read the file after obtaining the lock
	d, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	var doc yaml.Node
	err = yaml.Unmarshal(d, &doc)
	if err != nil {
		return err
	}

	services, err := servicesNode(&doc)
	if err != nil {
		return err
	}

	err = fn(services)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	err = enc.Encode(&doc)
	if err != nil {
		return err
	}
	err = enc.Close()
	if err != nil {
		return err
	}

	// Truncate the file before writing to it
	err = f.Truncate(0)
	if err != nil {
		return err
	}

	// Reset file pointer to beginning
	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}

	_, err = f.Write(buf.Bytes())
	return err
}

func servicesNode(doc *yaml.Node) (*yaml.Node, error) {
	if len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a mapping at the top level of the compose file")
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" {
			continue
		}
		services := root.Content[i+1]
		if services.Kind == yaml.ScalarNode && services.Tag == "!!null" {
			services.Kind, services.Tag, services.Value = yaml.MappingNode, "!!map", ""
		}
		if services.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("expected 'services' to be a mapping in the compose file")
		}
		services.Style = 0
		return services, nil
	}

	services := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "services"}, services)
	return services, nil
}
