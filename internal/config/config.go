package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration.
type Config struct {
	Services ServiceConfig `yaml:"services"`
	Proxy    ProxyConfig   `yaml:"proxy"`
	Git      GitConfig     `yaml:"git"`
	Deploy   DeployConfig  `yaml:"deploy"`
	Admin    AdminConfig   `yaml:"admin"`
	Health   HealthConfig  `yaml:"health"`
	Metrics  MetricsConfig `yaml:"metrics"`
}

// ServiceConfig defines the blue and green service endpoints.
type ServiceConfig struct {
	Blue  ServiceEndpoint `yaml:"blue"`
	Green ServiceEndpoint `yaml:"green"`
}

// ServiceEndpoint represents a single backend service.
type ServiceEndpoint struct {
	URL        string `yaml:"url"`
	HealthPath string `yaml:"health_path"`
}

// ProxyConfig defines the proxy server settings.
type ProxyConfig struct {
	ListenAddr   string        `yaml:"listen_addr"`
	DrainTimeout time.Duration `yaml:"drain_timeout"`
}

// GitConfig defines the git repository settings for deployment watching.
type GitConfig struct {
	RepoURL      string        `yaml:"repo_url"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Branch       string        `yaml:"branch"`
	AuthToken    string        `yaml:"auth_token"`
}

// DeployConfig defines the git tag patterns for blue/green deployments.
type DeployConfig struct {
	BlueTag   string `yaml:"blue_tag"`
	GreenTag  string `yaml:"green_tag"`
	ActiveTag string `yaml:"active_tag"`
}

// AdminConfig defines the Tailscale admin interface settings.
type AdminConfig struct {
	Hostname   string `yaml:"hostname"`
	StateDir   string `yaml:"state_dir"`
	AuthKey    string `yaml:"auth_key"`
	ControlURL string `yaml:"control_url"` // Custom control server (e.g., Headscale)
	Ephemeral  bool   `yaml:"ephemeral"`   // Node is removed when it goes offline
	WebhookKey string `yaml:"webhook_key"` // Secret for webhook signature verification
}

// HealthConfig defines the health check settings.
type HealthConfig struct {
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// MetricsConfig defines the metrics collection settings.
type MetricsConfig struct {
	HistoryDuration   time.Duration `yaml:"history_duration"`
	HistoryResolution time.Duration `yaml:"history_resolution"`
}

// Load reads the configuration from a YAML file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand environment variables
	data = []byte(os.ExpandEnv(string(data)))

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	cfg.setDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// setDefaults applies default values to the configuration.
func (c *Config) setDefaults() {
	if c.Proxy.ListenAddr == "" {
		c.Proxy.ListenAddr = ":8080"
	}
	if c.Proxy.DrainTimeout == 0 {
		c.Proxy.DrainTimeout = 30 * time.Second
	}
	if c.Git.PollInterval == 0 {
		c.Git.PollInterval = 30 * time.Second
	}
	if c.Git.Branch == "" {
		c.Git.Branch = "main"
	}
	if c.Deploy.BlueTag == "" {
		c.Deploy.BlueTag = "deploy/blue"
	}
	if c.Deploy.GreenTag == "" {
		c.Deploy.GreenTag = "deploy/green"
	}
	if c.Deploy.ActiveTag == "" {
		c.Deploy.ActiveTag = "deploy/active"
	}
	if c.Admin.Hostname == "" {
		c.Admin.Hostname = "bluegreen-admin"
	}
	if c.Admin.StateDir == "" {
		c.Admin.StateDir = "/var/lib/bluegreen/tailscale"
	}
	if c.Health.Interval == 0 {
		c.Health.Interval = 10 * time.Second
	}
	if c.Health.Timeout == 0 {
		c.Health.Timeout = 5 * time.Second
	}
	if c.Metrics.HistoryDuration == 0 {
		c.Metrics.HistoryDuration = 24 * time.Hour
	}
	if c.Metrics.HistoryResolution == 0 {
		c.Metrics.HistoryResolution = 1 * time.Minute
	}
	if c.Services.Blue.HealthPath == "" {
		c.Services.Blue.HealthPath = "/health"
	}
	if c.Services.Green.HealthPath == "" {
		c.Services.Green.HealthPath = "/health"
	}
}

// validate checks the configuration for required fields and valid values.
func (c *Config) validate() error {
	if c.Services.Blue.URL == "" {
		return fmt.Errorf("services.blue.url is required")
	}
	if c.Services.Green.URL == "" {
		return fmt.Errorf("services.green.url is required")
	}
	if c.Proxy.DrainTimeout < 0 {
		return fmt.Errorf("proxy.drain_timeout must be non-negative")
	}
	if c.Git.PollInterval < time.Second {
		return fmt.Errorf("git.poll_interval must be at least 1 second")
	}
	if c.Health.Interval < time.Second {
		return fmt.Errorf("health.interval must be at least 1 second")
	}
	if c.Health.Timeout < 100*time.Millisecond {
		return fmt.Errorf("health.timeout must be at least 100ms")
	}
	if c.Health.Timeout >= c.Health.Interval {
		return fmt.Errorf("health.timeout must be less than health.interval")
	}
	return nil
}

// ServiceTarget represents which backend service is targeted.
type ServiceTarget string

const (
	ServiceBlue  ServiceTarget = "blue"
	ServiceGreen ServiceTarget = "green"
)

// GetServiceEndpoint returns the endpoint configuration for the given target.
func (c *Config) GetServiceEndpoint(target ServiceTarget) ServiceEndpoint {
	switch target {
	case ServiceBlue:
		return c.Services.Blue
	case ServiceGreen:
		return c.Services.Green
	default:
		return ServiceEndpoint{}
	}
}
