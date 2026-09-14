package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
)

const apiMaxAttempts = 8

// isRetryableAPI проверяет, стоит ли повторять запрос при данной ошибке.
func isRetryableAPI(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case http.StatusForbidden, // 403 — часто rate limit
			http.StatusTooManyRequests,     // 429
			http.StatusInternalServerError, // 500
			http.StatusBadGateway,          // 502
			http.StatusServiceUnavailable,  // 503
			http.StatusGatewayTimeout:      // 504
			return true
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") {
		return true
	}
	return false
}

// withRetry выполняет операцию с retry и экспоненциальной задержкой.
func withRetry(ctx context.Context, opName string, op func() error) error {
	var lastErr error
	for attempt := 1; attempt <= apiMaxAttempts; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableAPI(err) {
			return err
		}

		wait := time.Duration(1<<uint(attempt-1)) * time.Second
		if wait > 60*time.Second {
			wait = 60 * time.Second
		}
		log.Printf("⚠️  %s: попытка %d/%d, ошибка: %v. Ждём %v...",
			opName, attempt, apiMaxAttempts, err, wait)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s: после %d попыток не удалось: %w", opName, apiMaxAttempts, lastErr)
}
