package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
)

// WebhookConfig holds webhook authentication settings.
type WebhookConfig struct {
	Secret string // HMAC secret for webhook verification
}

// WebhookRefreshInput is the request body for POST /api/webhook/refresh
type WebhookRefreshInput struct {
	RawBody []byte
	Body    struct {
		Signature string `json:"signature,omitempty" doc:"HMAC signature for verification"`
	}
	Headers struct {
		XHubSignature256 string `header:"X-Hub-Signature-256" doc:"GitHub-style HMAC signature"`
		XWebhookSecret   string `header:"X-Webhook-Secret" doc:"Simple secret header"`
	}
}

// WebhookRefreshOutput is the response for POST /api/webhook/refresh
type WebhookRefreshOutput struct {
	Body switcher.RefreshGitResult
}

// RegisterWebhookAPI registers webhook API operations.
func RegisterWebhookAPI(api huma.API, s *Server, webhookSecret string) {
	if webhookSecret == "" {
		s.logger.Warn("webhook authentication disabled - no secret configured")
	}

	// POST /api/webhook/refresh - triggers a git refresh which may cause a switch
	huma.Post(api, "/api/webhook/refresh", func(ctx context.Context, input *WebhookRefreshInput) (*WebhookRefreshOutput, error) {
		// Verify webhook signature if secret is configured
		if webhookSecret != "" {
			if !verifyWebhookRefresh(input, webhookSecret) {
				return nil, huma.Error401Unauthorized("invalid webhook signature")
			}
		}

		result := s.switcher.RefreshGit(ctx)

		// Return 500 only on complete failure. Partial success (Refreshed=true with Error)
		// returns 200 with error details in the response body.
		if result.Error != "" && !result.Refreshed {
			return nil, huma.Error500InternalServerError(result.Error)
		}

		return &WebhookRefreshOutput{Body: result}, nil
	})
}

// verifyWebhookRefresh verifies the webhook signature for refresh requests.
func verifyWebhookRefresh(input *WebhookRefreshInput, secret string) bool {
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

// ServeHTTP implements http.Handler for git refresh webhooks.
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

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Trigger git refresh - any tag changes will cause switches via the callback
	result := h.switcher.RefreshGit(ctx)

	if result.Error != "" && !result.Refreshed {
		http.Error(w, result.Error, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		// Response already started, can only log
		// Error will be logged by middleware or handled upstream
		return
	}
}
