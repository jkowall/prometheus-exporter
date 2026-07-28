package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/logging"
)

// SignatureHeader is the header Spacelift signs outgoing webhook payloads
// with. It is an HMAC-SHA256 of the raw request body, keyed with the webhook
// endpoint's secret, formatted as "sha256=<hex>" the same way GitHub does it.
//
// Spacelift also sends X-Signature (HMAC-SHA1), but that one is omitted in FIPS
// environments, so we verify the SHA-256 header only.
const SignatureHeader = "X-Signature-256"

const signaturePrefix = "sha256="

// maxBodyBytes caps how much of a delivery we will read. Payloads carry a
// run's resource changes and policy receipts, so a large plan produces a large
// body, but this bounds memory against a malicious or misconfigured sender.
const maxBodyBytes = 8 << 20 // 8 MiB

var (
	errMissingSignature = errors.New("missing " + SignatureHeader + " header")
	errBadSignature     = errors.New("signature does not match the configured secret")
)

// Handler receives Spacelift notification policy deliveries and records them
// as Prometheus metrics.
type Handler struct {
	metrics *Metrics
	secret  []byte
	logger  *zap.SugaredLogger
	now     func() time.Time
}

// NewHandler returns a Handler that verifies deliveries against secret.
//
// The secret must be the one configured on the Spacelift named webhook. An
// empty secret is rejected: an unauthenticated endpoint would let anyone inject
// arbitrary run metrics, including labels, into the exporter.
//
// The logger is resolved from ctx once here rather than per request, because
// incoming request contexts do not carry one and logging.FromContext falls back
// to a package global that is nil until logging.Init has run.
func NewHandler(ctx context.Context, metrics *Metrics, secret string) (*Handler, error) {
	if secret == "" {
		return nil, errors.New("a webhook secret is required so that deliveries can be authenticated")
	}

	logger := logging.FromContext(ctx)
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Handler{
		metrics: metrics,
		secret:  []byte(secret),
		logger:  logger.Sugar(),
		now:     time.Now,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := h.logger

	if r.Method != http.MethodPost {
		h.metrics.recordDelivery(resultMethodNotAllowed)
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "only POST is accepted", http.StatusMethodNotAllowed)

		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		h.metrics.recordDelivery(resultMalformed)
		h.metrics.recordPayloadError()
		logger.Warnw("Could not read webhook body", zap.Error(err))
		http.Error(w, "could not read request body", http.StatusBadRequest)

		return
	}

	if err := h.verify(r.Header.Get(SignatureHeader), body); err != nil {
		h.metrics.recordDelivery(resultBadSignature)
		// Deliberately terse to the caller: a verification oracle should
		// not explain itself. The detail goes to the log instead.
		logger.Warnw("Rejected webhook delivery", zap.Error(err))
		http.Error(w, "invalid signature", http.StatusUnauthorized)

		return
	}

	var payload Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.metrics.recordDelivery(resultMalformed)
		h.metrics.recordPayloadError()
		logger.Warnw("Could not decode webhook payload", zap.Error(err))
		http.Error(w, "could not decode payload", http.StatusBadRequest)

		return
	}

	// Only terminated runs carry complete timing, so anything else is
	// acknowledged and dropped. Returning 2xx matters: a non-2xx response
	// counts towards the consecutive-failure limit that makes Spacelift
	// disable the webhook.
	if payload.RunUpdated == nil || !payload.RunUpdated.Run.IsTerminal() {
		h.metrics.recordDelivery(resultIgnored)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ignored: not a terminated run\n")

		return
	}

	h.metrics.RecordRun(&payload)
	h.metrics.recordDelivery(resultAccepted)
	h.metrics.recordDeliveryTimestamp(float64(h.now().UnixNano()) / nanosecondsPerSecond)

	logger.Debugw("Recorded run",
		"run", payload.RunUpdated.Run.ID,
		"stack", payload.RunUpdated.Stack.ID,
		"state", payload.RunUpdated.Run.State,
	)

	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "recorded\n")
}

// verify checks the HMAC-SHA256 signature of body against the configured
// secret in constant time.
func (h *Handler) verify(header string, body []byte) error {
	if header == "" {
		return errMissingSignature
	}

	if !strings.HasPrefix(header, signaturePrefix) {
		return fmt.Errorf("signature %q is not prefixed with %q", header, signaturePrefix)
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return fmt.Errorf("signature is not valid hex: %w", err)
	}

	mac := hmac.New(sha256.New, h.secret)
	if _, err := mac.Write(body); err != nil {
		return fmt.Errorf("could not compute signature: %w", err)
	}

	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errBadSignature
	}

	return nil
}

// Sign returns the value Spacelift would send in the X-Signature-256 header
// for a given body and secret. It exists so tests, and anyone debugging a
// delivery by hand, can produce a valid signature.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
