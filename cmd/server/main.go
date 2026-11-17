package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/roman/kidstracker/internal/api"
	"github.com/roman/kidstracker/internal/mqtt"
	"github.com/roman/kidstracker/internal/secrets"
	"github.com/roman/kidstracker/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	adminToken := os.Getenv("ADMIN_TOKEN")
	if len(adminToken) < 32 {
		logger.Error("ADMIN_TOKEN must be set to at least 32 characters")
		os.Exit(1)
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Error("DATABASE_URL must be set")
		os.Exit(1)
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8080"
	}
	if !strings.HasPrefix(address, "127.0.0.1:") && !strings.HasPrefix(address, "localhost:") {
		logger.Warn("server is listening on a non-loopback interface; use HTTPS behind a trusted reverse proxy")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo, err := store.Open(ctx, databaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}

	settingsCipher, err := secrets.New(adminToken)
	if err != nil {
		logger.Error("could not initialize settings encryption", "error", err)
		os.Exit(1)
	}
	mqttManager := mqtt.NewManager(ctx, logger)
	record, err := repo.GetMQTTSettings(ctx)
	if err != nil {
		logger.Warn("could not load MQTT settings", "error", err)
	} else {
		password := ""
		if len(record.PasswordCiphertext) > 0 {
			plaintext, decryptErr := settingsCipher.Decrypt(record.PasswordCiphertext)
			if decryptErr != nil {
				logger.Warn("could not decrypt MQTT password; re-enter it in the admin UI", "error", decryptErr)
			} else {
				password = string(plaintext)
			}
		}
		if err := mqttManager.Configure(record.Settings, password); err != nil {
			logger.Warn("could not configure MQTT", "error", err)
		}
	}
	if devices, err := repo.ListDevices(ctx); err == nil {
		for _, device := range devices {
			mqttManager.PublishDevice(device)
			if device.CurrentLocation != nil {
				mqttManager.PublishLocation(device, *device.CurrentLocation)
			}
		}
	}

	server := &http.Server{Addr: address, Handler: api.New(repo, adminToken, mqttManager), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		logger.Info("server listening", "address", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
