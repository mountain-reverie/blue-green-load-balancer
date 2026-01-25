package headscale

import "fmt"

// generateHeadscaleConfig returns a minimal Headscale configuration for testing.
// serverURL should be the externally accessible URL (e.g., http://localhost:32768).
func generateHeadscaleConfig(serverURL string) string {
	return fmt.Sprintf(`
server_url: %s
listen_addr: 0.0.0.0:8080
metrics_listen_addr: 0.0.0.0:9090
grpc_listen_addr: 0.0.0.0:50443
grpc_allow_insecure: true

database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite

noise:
  private_key_path: /var/lib/headscale/noise_private.key

prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48

derp:
  server:
    enabled: false
  urls:
    - https://controlplane.tailscale.com/derpmap/default

dns:
  magic_dns: true
  base_domain: test.headscale.net

policy:
  path: /etc/headscale/acl.json
`, serverURL)
}

// generateHeadscaleACL returns a permissive ACL policy for testing.
func generateHeadscaleACL() string {
	return `{
  "acls": [
    {
      "action": "accept",
      "src": ["*"],
      "dst": ["*:*"]
    }
  ]
}`
}
