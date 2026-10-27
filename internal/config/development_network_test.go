package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Local development databases and the MinIO admin console must not be published
// on every host interface. Remote developers can explicitly use an SSH tunnel.
func TestDevelopmentServicesPublishOnlyOnLoopback(t *testing.T) {
	raw, err := os.ReadFile("../../docker-compose.dev.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"postgres", "redis", "minio"} {
		item, ok := config.Services[service]
		if !ok || len(item.Ports) == 0 {
			t.Fatalf("missing published ports for %s", service)
		}
	}
	for name, service := range config.Services {
		for _, port := range service.Ports {
			if !strings.HasPrefix(port, "127.0.0.1:") && !strings.HasPrefix(port, "[::1]:") {
				t.Errorf("%s publishes %q outside loopback", name, port)
			}
		}
	}
}
