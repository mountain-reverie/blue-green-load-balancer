//go:build integration

package headscale

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Container wraps a Headscale testcontainer.
type Container struct {
	testcontainers.Container
	URL string // http://host:port
}

// StartHeadscale starts a Headscale container for testing.
func StartHeadscale(ctx context.Context) (*Container, error) {
	configContent := generateHeadscaleConfig()

	req := testcontainers.ContainerRequest{
		Image:        "headscale/headscale:0.23",
		ExposedPorts: []string{"8080/tcp", "3478/udp"},
		Cmd:          []string{"serve"},
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(configContent),
			ContainerFilePath: "/etc/headscale/config.yaml",
			FileMode:          0644,
		}},
		WaitingFor: wait.ForAll(
			wait.ForLog("Listening on"),
			wait.ForListeningPort("8080/tcp"),
		).WithDeadline(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
	if err != nil {
		return nil, fmt.Errorf("starting Headscale container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("getting Headscale host: %w", err)
	}

	port, err := container.MappedPort(ctx, "8080")
	if err != nil {
		container.Terminate(ctx)
		return nil, fmt.Errorf("getting Headscale port: %w", err)
	}

	return &Container{
		Container: container,
		URL:       fmt.Sprintf("http://%s:%s", host, port.Port()),
	}, nil
}

// CreateUser creates a new user in Headscale.
func (h *Container) CreateUser(ctx context.Context, username string) error {
	exitCode, _, err := h.Exec(ctx, []string{"headscale", "users", "create", username})
	if err != nil {
		return fmt.Errorf("executing user create: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("user create exited with code %d", exitCode)
	}
	return nil
}

// CreatePreauthKey creates a reusable, ephemeral preauth key for a user.
func (h *Container) CreatePreauthKey(ctx context.Context, user string) (string, error) {
	exitCode, reader, err := h.Exec(ctx, []string{
		"headscale", "preauthkeys", "create",
		"--user", user,
		"--reusable",
		"--ephemeral",
		"--expiration", "1h",
	})
	if err != nil {
		return "", fmt.Errorf("executing preauthkey create: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("preauthkey create exited with code %d", exitCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return "", fmt.Errorf("reading exec output: %w", err)
	}

	key, err := parseKeyFromOutput(buf.String())
	if err != nil {
		return "", fmt.Errorf("parsing key from output: %w", err)
	}

	return key, nil
}

// parseKeyFromOutput extracts the preauth key from Headscale CLI output.
// The output format varies by version, so we try multiple patterns.
func parseKeyFromOutput(output string) (string, error) {
	// Pattern 1: Key is on a line by itself (newer versions)
	// Pattern 2: Key follows "key:" or "Key:" label
	// Pattern 3: Key is in a table format

	// Keys typically start with certain prefixes
	keyPatterns := []*regexp.Regexp{
		regexp.MustCompile(`([a-f0-9]{48})`),                     // 48-char hex key
		regexp.MustCompile(`(nodekey:[a-f0-9]+)`),                // nodekey format
		regexp.MustCompile(`(?i)key[:\s]+([a-f0-9]{48})`),        // labeled key
		regexp.MustCompile(`\|\s*([a-f0-9]{48})\s*\|`),           // table format
		regexp.MustCompile(`([a-zA-Z0-9]{43,})`),                 // base64-like key
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		for _, pattern := range keyPatterns {
			if matches := pattern.FindStringSubmatch(line); len(matches) > 1 {
				return matches[1], nil
			}
		}
	}

	return "", fmt.Errorf("no key found in output: %s", output)
}

// GetTailscaleIP retrieves the Tailscale IP for a given hostname.
func (h *Container) GetTailscaleIP(ctx context.Context, hostname string) (string, error) {
	exitCode, reader, err := h.Exec(ctx, []string{
		"headscale", "nodes", "list", "-o", "json-line",
	})
	if err != nil {
		return "", fmt.Errorf("executing nodes list: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("nodes list exited with code %d", exitCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return "", fmt.Errorf("reading exec output: %w", err)
	}

	// Parse the output to find the IP for the given hostname
	// This is a simplified approach; in production you'd want proper JSON parsing
	lines := strings.Split(buf.String(), "\n")
	for _, line := range lines {
		if strings.Contains(line, hostname) {
			// Look for IP pattern in the line
			ipPattern := regexp.MustCompile(`100\.64\.\d+\.\d+`)
			if match := ipPattern.FindString(line); match != "" {
				return match, nil
			}
		}
	}

	return "", fmt.Errorf("no IP found for hostname %s", hostname)
}
