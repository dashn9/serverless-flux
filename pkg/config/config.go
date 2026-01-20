package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type FluxConfig struct {
	RedisAddr string        `yaml:"redis_addr"`
	Agents    []AgentConfig `yaml:"agents"`
}

type AgentConfig struct {
	ID             string `yaml:"id"`
	Address        string `yaml:"address"`
	MaxConcurrency int32  `yaml:"max_concurrency"`
}

func LoadFluxConfig(path string) (*FluxConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config FluxConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
