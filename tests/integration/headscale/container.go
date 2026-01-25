package headscale

import (
	"bytes"
	"context"
	"encoding/json"
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

// headscaleTestPort is the fixed port used for Headscale in tests.
// Using a fixed port simplifies config generation (server_url needs to be known at startup).
const headscaleTestPort = "19080"

// StartHeadscale starts a Headscale container for testing.
func StartHeadscale(ctx context.Context) (*Container, error) {
	// Use localhost with fixed port for the server URL
	serverURL := fmt.Sprintf("http://localhost:%s", headscaleTestPort)
	configContent := generateHeadscaleConfig(serverURL)

	aclContent := generateHeadscaleACL()

	req := testcontainers.ContainerRequest{
		Image:        "headscale/headscale:0.23",
		ExposedPorts: []string{headscaleTestPort + ":8080/tcp"},
		Cmd:          []string{"serve"},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(configContent),
				ContainerFilePath: "/etc/headscale/config.yaml",
				FileMode:          0644,
			},
			{
				Reader:            strings.NewReader(aclContent),
				ContainerFilePath: "/etc/headscale/acl.json",
				FileMode:          0644,
			},
		},
		WaitingFor: wait.ForAll(
			wait.ForLog("listening and serving HTTP on"),
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

	return &Container{
		Container: container,
		URL:       serverURL,
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

// preauthKeyResponse represents the JSON response from headscale preauthkeys create.
type preauthKeyResponse struct {
	Key string `json:"key"`
}

// CreatePreauthKey creates a reusable, ephemeral preauth key for a user.
func (h *Container) CreatePreauthKey(ctx context.Context, user string) (string, error) {
	exitCode, reader, err := h.Exec(ctx, []string{
		"headscale", "preauthkeys", "create",
		"--user", user,
		"--reusable",
		"--ephemeral",
		"--expiration", "1h",
		"-o", "json",
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

	// Docker exec output may contain multiplexed stream headers.
	// Find the start of JSON content.
	output := buf.Bytes()
	jsonStart := bytes.Index(output, []byte("{"))
	if jsonStart == -1 {
		return "", fmt.Errorf("no JSON found in output: %s", buf.String())
	}

	var resp preauthKeyResponse
	if err := json.Unmarshal(output[jsonStart:], &resp); err != nil {
		return "", fmt.Errorf("parsing JSON response: %w (output: %s)", err, string(output[jsonStart:]))
	}

	if resp.Key == "" {
		return "", fmt.Errorf("empty key in response: %s", buf.String())
	}

	return resp.Key, nil
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
