package cloudflare

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteRuntime writes tunnel-specific credentials and a local ingress config.
// Neither file grants account-wide Cloudflare access.
func WriteRuntime(dir string, tunnel Tunnel, hostname, origin string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create MCP state: %w", err)
	}
	credential := struct{ AccountTag, TunnelSecret, TunnelID string }{tunnel.AccountID, tunnel.Secret, tunnel.ID}
	b, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tunnel.json"), b, 0o600); err != nil {
		return fmt.Errorf("write tunnel credential: %w", err)
	}
	config := "tunnel: " + tunnel.ID + "\ningress:\n  - hostname: " + hostname + "\n    service: http://" + origin + "\n  - service: http_status:404\n"
	if err := os.WriteFile(filepath.Join(dir, "cloudflared.yml"), []byte(config), 0o600); err != nil {
		return fmt.Errorf("write tunnel config: %w", err)
	}
	return nil
}
