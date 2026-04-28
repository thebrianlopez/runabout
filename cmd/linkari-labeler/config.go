package main

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

type LabelerConfig struct {
	LabelerDID                 string `yaml:"labeler_did"`
	LabelerSigningKeyMultibase string `yaml:"labeler_signing_key_multibase"`
	QueueDBPath                string `yaml:"queue_db_path"`
	ListenAddr                 string `yaml:"listen_addr"`
}

func defaultLabelerConfig() *LabelerConfig {
	return &LabelerConfig{
		LabelerDID:  "did:plc:placeholder",
		ListenAddr:  ":7800",
		QueueDBPath: "~/.config/linkari/queue.db",
	}
}

func loadLabelerConfig(path string) (*LabelerConfig, error) {
	cfg := defaultLabelerConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	return cfg, yaml.Unmarshal(data, cfg)
}

func writeLabelerConfig(path string, cfg *LabelerConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func validateLabelerConfig(cfg *LabelerConfig) error {
	if cfg.LabelerSigningKeyMultibase == "" {
		return errors.New("labeler_key_missing")
	}
	return nil
}
