package headscale

// generateHeadscaleConfig returns a minimal Headscale configuration for testing.
func generateHeadscaleConfig() string {
	return `
server_url: http://headscale:8080
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
    enabled: true
    region_id: 999
    region_code: "test"
    region_name: "Test"
    stun_listen_addr: 0.0.0.0:3478
  urls: []

dns:
  magic_dns: true
  base_domain: test.headscale.net
`
}
