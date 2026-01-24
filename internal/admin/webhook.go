package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
)

// WebhookConfig holds webhook authentication settings.
type WebhookConfig struct {
	Secret string // HMAC secret for webhook verification
}

// WebhookSwitchInput is the request body for POST /api/webhook/switch
type WebhookSwitchInput struct {
	RawBody []byte
	Body    struct {
		Target    string `json:"target" enum:"blue,green" required:"true" doc:"Target service to switch to"`
		Signature string `json:"signature,omitempty" doc:"HMAC signature for verification"`
	}
	Headers struct {
		XHubSignature256 string `header:"X-Hub-Signature-256" doc:"GitHub-style HMAC signature"`
		XWebhookSecret   string `header:"X-Webhook-Secret" doc:"Simple secret header"`
	}
}

// WebhookSwitchOutput is the response for POST /api/webhook/switch
type WebhookSwitchOutput struct {
	Body struct {
		Success  bool   `json:"success" example:"true"`
		Previous string `json:"previous" example:"blue"`
		Current  string `json:"current" example:"green"`
		Message  string `json:"message,omitempty"`
	}
}

// RegisterWebhookAPI registers webhook API operations.
func RegisterWebhookAPI(api huma.API, s *Server, webhookSecret string) {
	// POST /api/webhook/switch
	huma.Post(api, "/api/webhook/switch", func(ctx context.Context, input *WebhookSwitchInput) (*WebhookSwitchOutput, error) {
		// Verify webhook signature if secret is configured
		if webhookSecret != "" {
			if !verifyWebhook(input, webhookSecret) {
				return nil, huma.Error401Unauthorized("invalid webhook signature")
			}
		}

		var target config.ServiceTarget
		switch input.Body.Target {
		case "blue":
			target = config.ServiceBlue
		case "green":
			target = config.ServiceGreen
		default:
			return nil, huma.Error400BadRequest(fmt.Sprintf("invalid target: %s", input.Body.Target))
		}

		previous := s.switcher.ActiveTarget()

		if err := s.switcher.Switch(ctx, target, switcher.TriggerWebhook); err != nil {
			return nil, huma.Error500InternalServerError(err.Error())
		}

		out := &WebhookSwitchOutput{}
		out.Body.Success = true
		out.Body.Previous = string(previous)
		out.Body.Current = string(target)
		out.Body.Message = fmt.Sprintf("Webhook triggered switch from %s to %s", previous, target)

		return out, nil
	})
}

// verifyWebhook verifies the webhook signature.
func verifyWebhook(input *WebhookSwitchInput, secret string) bool {
	// Check X-Hub-Signature-256 header (GitHub style)
	if input.Headers.XHubSignature256 != "" {
		return verifyHubSignature(input.RawBody, input.Headers.XHubSignature256, secret)
	}

	// Check X-Webhook-Secret header (simple)
	if input.Headers.XWebhookSecret != "" {
		return subtle.ConstantTimeCompare([]byte(input.Headers.XWebhookSecret), []byte(secret)) == 1
	}

	// Check signature in body
	if input.Body.Signature != "" {
		return subtle.ConstantTimeCompare([]byte(input.Body.Signature), []byte(secret)) == 1
	}

	return false
}

// verifyHubSignature verifies a GitHub-style HMAC signature.
func verifyHubSignature(payload []byte, signature string, secret string) bool {
	// Signature format: sha256=<hex>
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}

	sigHex := strings.TrimPrefix(signature, "sha256=")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedMAC := mac.Sum(nil)

	return hmac.Equal(sigBytes, expectedMAC)
}

// WebhookHandler provides a standard http.Handler for webhooks.
// This can be used outside of Huma if needed.
type WebhookHandler struct {
	switcher      *switcher.Switcher
	webhookSecret string
}

// NewWebhookHandler creates a new webhook handler.
func NewWebhookHandler(sw *switcher.Switcher, secret string) *WebhookHandler {
	return &WebhookHandler{
		switcher:      sw,
		webhookSecret: secret,
	}
}

// maxWebhookBodySize is the maximum size of a webhook request body (1MB).
const maxWebhookBodySize = 1 << 20

// ServeHTTP implements http.Handler.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Limit request body size to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Verify signature when secret is configured
	if h.webhookSecret != "" {
		signature := r.Header.Get("X-Hub-Signature-256")
		secretHeader := r.Header.Get("X-Webhook-Secret")

		// Require at least one authentication method
		if signature == "" && secretHeader == "" {
			http.Error(w, "unauthorized: missing signature", http.StatusUnauthorized)
			return
		}

		// Verify HMAC signature if provided
		if signature != "" {
			if !verifyHubSignature(body, signature, h.webhookSecret) {
				http.Error(w, "unauthorized: invalid signature", http.StatusUnauthorized)
				return
			}
		} else if secretHeader != "" {
			// Verify simple secret header
			if subtle.ConstantTimeCompare([]byte(secretHeader), []byte(h.webhookSecret)) != 1 {
				http.Error(w, "unauthorized: invalid secret", http.StatusUnauthorized)
				return
			}
		}
	}

	// Parse target from query param or simple body
	target := r.URL.Query().Get("target")
	if target == "" {
		target = strings.TrimSpace(string(body))
	}

	var serviceTarget config.ServiceTarget
	switch target {
	case "blue":
		serviceTarget = config.ServiceBlue
	case "green":
		serviceTarget = config.ServiceGreen
	default:
		http.Error(w, "invalid target", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.switcher.Switch(ctx, serviceTarget, switcher.TriggerWebhook); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "switched to %s", target)
}
