package headscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// headscaleNode represents a node in the Headscale nodes list JSON output.
type headscaleNode struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	GivenName   string   `json:"givenName"`
	IPAddresses []string `json:"ipAddresses"`
	Online      bool     `json:"online"`
}

// GetTailscaleIP retrieves the Tailscale IP for a given hostname.
// It only returns IPs for online nodes and matches the hostname exactly.
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

	output := buf.String()
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("no nodes found for hostname %s", hostname)
	}

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Docker exec output may contain multiplexed stream headers before JSON.
		// Find the start of JSON content on this line.
		jsonStart := strings.Index(line, "{")
		if jsonStart == -1 {
			continue
		}
		line = line[jsonStart:]

		var node headscaleNode
		if err := json.Unmarshal([]byte(line), &node); err != nil {
			// Skip malformed lines
			continue
		}

		// Match hostname exactly against both name and givenName fields
		if node.Name != hostname && node.GivenName != hostname {
			continue
		}

		// Only return IPs for online nodes
		if !node.Online {
			continue
		}

		// Return the first IP address (typically the IPv4 address)
		if len(node.IPAddresses) == 0 {
			return "", fmt.Errorf("node %s has no IP addresses", hostname)
		}

		return node.IPAddresses[0], nil
	}

	return "", fmt.Errorf("no online node found for hostname %s", hostname)
}
