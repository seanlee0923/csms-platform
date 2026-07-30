package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/seanlee0923/csms-platform/internal/commandbus"
	"github.com/seanlee0923/csms-platform/internal/sessionregistry"
)

const commandTimeout = 30 * time.Second

func serverCommandHandler(apiKeys []string, ownership *ownershipManager, bus commandbus.Bus, limiter commandRateLimiter, logger *slog.Logger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodPost {
			writeAPIError(response, http.StatusMethodNotAllowed, "MethodNotAllowed", "POST is required")
			return
		}
		if len(apiKeys) == 0 || ownership == nil || bus == nil || limiter == nil {
			writeAPIError(response, http.StatusServiceUnavailable, "CommandAPIUnavailable", "command API is not configured")
			return
		}
		keyID, authenticated := authenticateBearer(request.Header.Get("Authorization"), apiKeys)
		if !authenticated {
			logger.Warn("command audit", "outcome", "unauthorized", "remote_address", request.RemoteAddr)
			writeAPIError(response, http.StatusUnauthorized, "Unauthorized", "valid bearer credentials are required")
			return
		}
		allowed, err := limiter.Allow(request.Context(), keyID)
		if err != nil {
			logger.Error("command audit", "outcome", "rate_limit_failure", "credential_id", keyID, "error", err)
			writeAPIError(response, http.StatusServiceUnavailable, "RateLimitUnavailable", "command admission is unavailable")
			return
		}
		if !allowed {
			logger.Warn("command audit", "outcome", "rate_limited", "credential_id", keyID)
			response.Header().Set("Retry-After", "60")
			writeAPIError(response, http.StatusTooManyRequests, "RateLimited", "command rate limit exceeded")
			return
		}
		identity, ok := resetPathIdentity(request.URL.Path)
		if !ok {
			writeAPIError(response, http.StatusNotFound, "NotFound", "command endpoint was not found")
			return
		}
		var payload resetCommand
		decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeAPIError(response, http.StatusBadRequest, "InvalidPayload", err.Error())
			return
		}
		lease, err := ownership.registry.Lookup(request.Context(), identity)
		if err != nil {
			status := http.StatusInternalServerError
			code := "RegistryFailure"
			if errors.Is(err, sessionregistry.ErrNotFound) {
				status, code = http.StatusConflict, "StationNotConnected"
			}
			writeAPIError(response, status, code, err.Error())
			return
		}
		now := time.Now().UTC()
		command := commandbus.Command{
			ID: newCommandID(), StationIdentity: identity, OwnerID: lease.OwnerID,
			OwnerGeneration: lease.Generation, Action: resetAction,
			Payload: mustJSON(payload), CreatedAt: now, Deadline: now.Add(commandTimeout),
		}
		if err := bus.Publish(request.Context(), command); err != nil {
			logger.Error("command audit", "outcome", "publish_failed", "command_id", command.ID,
				"identity", identity, "action", command.Action, "credential_id", keyID, "error", err)
			writeAPIError(response, http.StatusServiceUnavailable, "CommandPublishFailed", err.Error())
			return
		}
		logger.Info("command audit", "outcome", "published", "command_id", command.ID,
			"identity", identity, "action", command.Action, "owner_id", lease.OwnerID,
			"owner_generation", lease.Generation, "credential_id", keyID)
		waitCtx, cancel := context.WithDeadline(request.Context(), command.Deadline)
		defer cancel()
		result, err := bus.AwaitResult(waitCtx, command.ID)
		if err != nil {
			logger.Warn("command audit", "outcome", "timeout", "command_id", command.ID,
				"identity", identity, "action", command.Action, "credential_id", keyID)
			writeAPIError(response, http.StatusGatewayTimeout, "CommandTimeout", err.Error())
			return
		}
		status := http.StatusOK
		if !result.Success {
			status = http.StatusConflict
		}
		logger.Info("command audit", "outcome", "completed", "command_id", command.ID,
			"identity", identity, "action", command.Action, "success", result.Success,
			"error_code", result.ErrorCode, "credential_id", keyID)
		response.WriteHeader(status)
		_ = json.NewEncoder(response).Encode(result)
	}
}

func resetPathIdentity(path string) (string, bool) {
	const prefix = "/api/v1/stations/"
	const suffix = "/commands/reset"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	identity := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return identity, identity != "" && !strings.Contains(identity, "/")
}

func authenticateBearer(header string, apiKeys []string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	provided := strings.TrimPrefix(header, prefix)
	matched := 0
	for _, apiKey := range apiKeys {
		if len(provided) == len(apiKey) {
			matched |= subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey))
		}
	}
	if matched != 1 {
		return "", false
	}
	sum := sha256.Sum256([]byte(provided))
	return hex.EncodeToString(sum[:8]), true
}

func newCommandID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(value[:])
}

func mustJSON(value any) json.RawMessage {
	content, _ := json.Marshal(value)
	return content
}

func writeAPIError(response http.ResponseWriter, status int, code, description string) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"errorCode": code, "errorDescription": description})
}
