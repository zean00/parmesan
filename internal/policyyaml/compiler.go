package policyyaml

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sahal/parmesan/internal/domain/policy"
)

func ParseBundle(raw []byte) (policy.Bundle, error) {
	var bundle policy.Bundle
	if err := yaml.Unmarshal(raw, &bundle); err != nil {
		return policy.Bundle{}, fmt.Errorf("unmarshal yaml: %w", err)
	}

	bundle.SourceYAML = string(raw)
	bundle.ImportedAt = time.Now().UTC()

	if err := ValidateBundle(bundle); err != nil {
		return policy.Bundle{}, err
	}
	bundle.GuidelineToolAssociations = compileGuidelineToolAssociations(bundle)

	return bundle, nil
}

func ValidateBundle(bundle policy.Bundle) error {
	if strings.TrimSpace(bundle.ID) == "" {
		return errors.New("bundle.id is required")
	}
	if strings.TrimSpace(bundle.Version) == "" {
		return errors.New("bundle.version is required")
	}

	seen := map[string]struct{}{}
	for _, item := range bundle.Observations {
		if err := validateID("observation", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.When) == "" {
			return fmt.Errorf("observation %q requires when", item.ID)
		}
		if err := validateMCPRef(item.MCP); err != nil {
			return fmt.Errorf("observation %q: %w", item.ID, err)
		}
	}
	for _, item := range bundle.Guidelines {
		if err := validateID("guideline", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.When) == "" || strings.TrimSpace(item.Then) == "" {
			return fmt.Errorf("guideline %q requires when and then", item.ID)
		}
		if err := validateMCPRef(item.MCP); err != nil {
			return fmt.Errorf("guideline %q: %w", item.ID, err)
		}
	}
	for _, item := range bundle.Journeys {
		if err := validateID("journey", item.ID, seen); err != nil {
			return err
		}
		if len(item.When) == 0 {
			return fmt.Errorf("journey %q requires when", item.ID)
		}
		if len(item.States) == 0 {
			return fmt.Errorf("journey %q requires states", item.ID)
		}
		for _, state := range item.States {
			if strings.TrimSpace(state.ID) == "" || strings.TrimSpace(state.Type) == "" {
				return fmt.Errorf("journey %q has invalid state", item.ID)
			}
			if err := validateMCPRef(state.MCP); err != nil {
				return fmt.Errorf("journey %q state %q: %w", item.ID, state.ID, err)
			}
		}
		for _, guideline := range item.Guidelines {
			if err := validateID("journey guideline", guideline.ID, seen); err != nil {
				return err
			}
		}
		for _, template := range item.Templates {
			if err := validateID("journey template", template.ID, seen); err != nil {
				return err
			}
		}
	}
	for _, item := range bundle.Templates {
		if err := validateID("template", item.ID, seen); err != nil {
			return err
		}
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("template %q requires text", item.ID)
		}
	}
	for _, item := range bundle.ToolPolicies {
		if err := validateID("tool_policy", item.ID, seen); err != nil {
			return err
		}
		if len(item.ToolIDs) == 0 {
			return fmt.Errorf("tool_policy %q requires tool_ids", item.ID)
		}
	}

	return nil
}

func validateMCPRef(ref *policy.MCPRef) error {
	if ref == nil {
		return nil
	}
	if strings.TrimSpace(ref.Server) == "" {
		return errors.New("mcp.server is required when mcp is set")
	}
	if strings.TrimSpace(ref.Tool) != "" && len(ref.Tools) > 0 {
		return errors.New("mcp.tool and mcp.tools cannot both be set")
	}
	return nil
}

func validateID(kind, id string, seen map[string]struct{}) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%s id is required", kind)
	}
	if _, ok := seen[id]; ok {
		return fmt.Errorf("duplicate artifact id %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func compileGuidelineToolAssociations(bundle policy.Bundle) []policy.GuidelineToolAssociation {
	seen := map[string]struct{}{}
	var out []policy.GuidelineToolAssociation
	add := func(guidelineID string, toolID string) {
		guidelineID = strings.TrimSpace(guidelineID)
		toolID = strings.TrimSpace(toolID)
		if guidelineID == "" || toolID == "" {
			return
		}
		key := guidelineID + "::" + toolID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, policy.GuidelineToolAssociation{
			GuidelineID: guidelineID,
			ToolID:      toolID,
		})
	}

	addRefs := func(guidelineID string, tools []string, ref *policy.MCPRef) {
		for _, toolID := range tools {
			add(guidelineID, toolID)
		}
		if ref == nil {
			return
		}
		if strings.TrimSpace(ref.Tool) != "" {
			add(guidelineID, ref.Server+"."+ref.Tool)
		}
		for _, toolID := range ref.Tools {
			add(guidelineID, ref.Server+"."+toolID)
		}
		if strings.TrimSpace(ref.Server) != "" && strings.TrimSpace(ref.Tool) == "" && len(ref.Tools) == 0 {
			add(guidelineID, ref.Server+".*")
		}
	}

	for _, guideline := range bundle.Guidelines {
		addRefs(guideline.ID, guideline.Tools, guideline.MCP)
	}
	for _, flow := range bundle.Journeys {
		for _, guideline := range flow.Guidelines {
			addRefs(guideline.ID, guideline.Tools, guideline.MCP)
		}
		for _, state := range flow.States {
			projectedID := "journey_node:" + flow.ID + ":" + state.ID
			addRefs(projectedID, []string{state.Tool}, state.MCP)
		}
	}
	return out
}
