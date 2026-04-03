package parity

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadFixture(path string) (Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	var fx Fixture
	if err := dec.Decode(&fx); err != nil {
		return Fixture{}, err
	}
	if err := validateFixture(fx); err != nil {
		return Fixture{}, err
	}
	return fx, nil
}

func validateFixture(fx Fixture) error {
	if strings.TrimSpace(fx.Version) == "" {
		return errors.New("fixture version is required")
	}
	seen := map[string]struct{}{}
	for _, s := range fx.Scenarios {
		if strings.TrimSpace(s.ID) == "" {
			return errors.New("scenario id is required")
		}
		if _, ok := seen[s.ID]; ok {
			return fmt.Errorf("duplicate scenario id %q", s.ID)
		}
		seen[s.ID] = struct{}{}
		for _, turn := range s.Transcript {
			switch strings.TrimSpace(turn.Role) {
			case "customer", "agent":
			default:
				return fmt.Errorf("scenario %q has unsupported role %q", s.ID, turn.Role)
			}
		}
	}
	return nil
}
