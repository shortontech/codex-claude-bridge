package toolpolicy

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Policy struct {
	Enabled      map[string]struct{}
	Descriptions map[string]string
}

type filePolicy struct {
	EnabledTools []string          `yaml:"enabled_tools"`
	Descriptions map[string]string `yaml:"descriptions"`
}

func Load(path string) (Policy, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Policy{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Policy{}, nil
		}
		return Policy{}, err
	}
	var fp filePolicy
	if err := yaml.Unmarshal(b, &fp); err != nil {
		return Policy{}, fmt.Errorf("parse tool policy: %w", err)
	}
	policy := Policy{
		Enabled:      map[string]struct{}{},
		Descriptions: map[string]string{},
	}
	for _, name := range fp.EnabledTools {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			policy.Enabled[trimmed] = struct{}{}
		}
	}
	for name, desc := range fp.Descriptions {
		trimmedName := strings.TrimSpace(name)
		trimmedDesc := strings.TrimSpace(desc)
		if trimmedName != "" && trimmedDesc != "" {
			policy.Descriptions[trimmedName] = trimmedDesc
		}
	}
	if len(policy.Enabled) == 0 {
		policy.Enabled = nil
	}
	if len(policy.Descriptions) == 0 {
		policy.Descriptions = nil
	}
	return policy, nil
}

func (p Policy) IsEnabled(name string) bool {
	if len(p.Enabled) == 0 {
		return true
	}
	_, ok := p.Enabled[name]
	return ok
}

func (p Policy) Description(name, fallback string) string {
	if len(p.Descriptions) == 0 {
		return fallback
	}
	if d, ok := p.Descriptions[name]; ok {
		return d
	}
	return fallback
}
