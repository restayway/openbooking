package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"openbooking/pkg/appview"
	"openbooking/pkg/mesh"
	"openbooking/pkg/protocol"
	"openbooking/pkg/runtimex"
	appviewpostgres "openbooking/pkg/store/appviewpostgres"
)

func main() {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	registry := runtimex.NewRegistry()

	store, err := appviewpostgres.Open(cfg.postgresDSN)
	if err != nil {
		logger.Error("open postgres", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	service, err := appview.NewService(ctx, store, cfg.relayURL)
	if err != nil {
		logger.Error("init appview", "error", err)
		os.Exit(1)
	}
	meshClient, err := mesh.Connect(cfg.natsURL)
	if err != nil {
		logger.Error("connect nats", "error", err)
		os.Exit(1)
	}
	defer meshClient.Close()
	if err := meshClient.EnsureStream("OPENBOOKING_RELAY", "OBP.RELAY.*.COMMIT", "OBP.RELAY.*.CHECKPOINT"); err != nil {
		logger.Error("ensure relay stream", "error", err)
		os.Exit(1)
	}

	server := &http.Server{Addr: cfg.httpAddr, Handler: runtimex.ComposeHandler(appview.NewHandler(service), registry, func(snapshot runtimex.Snapshot) (bool, map[string]any) {
		lag := snapshot.Gauges["appview_replay_lag"]
		ready := snapshot.Components["postgres"].Healthy && snapshot.Components["replay_worker"].Healthy && snapshot.Components["live_follow_worker"].Healthy && lag <= cfg.readyReplayLagThreshold
		return ready, map[string]any{"ready": ready, "replayLag": lag, "components": snapshot.Components}
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		runtimex.RunRetriable(ctx, logger, registry, "replay_worker", runtimex.RetryConfig{InitialBackoff: time.Second, MaxBackoff: 15 * time.Second}, func(ctx context.Context) error {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if err := service.SyncReplay(ctx); err != nil {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
				}
			}
		})
	}()
	go func() {
		runtimex.RunRetriable(ctx, logger, registry, "live_follow_worker", runtimex.RetryConfig{InitialBackoff: time.Second, MaxBackoff: 15 * time.Second}, func(ctx context.Context) error {
			return consumeRelayEvents(ctx, service, meshClient, cfg.relayDID)
		})
	}()
	go monitorAppViewDependencies(ctx, registry, service, store, meshClient)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("openbooking-appview listening", "addr", cfg.httpAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("appview http server failed", "error", err)
		os.Exit(1)
	}
}

type config struct {
	httpAddr                string
	postgresDSN             string
	relayURL                string
	natsURL                 string
	relayDID                string
	readyReplayLagThreshold int64
}

func loadConfig() config {
	cfg := config{}
	flag.StringVar(&cfg.httpAddr, "http-addr", envOrDefault("OPENBOOKING_APPVIEW_HTTP_ADDR", ":8082"), "HTTP listen address")
	flag.StringVar(&cfg.postgresDSN, "postgres-dsn", envOrDefault("OPENBOOKING_APPVIEW_POSTGRES_DSN", ""), "PostgreSQL DSN")
	flag.StringVar(&cfg.relayURL, "relay-url", envOrDefault("OPENBOOKING_RELAY_URL", "http://127.0.0.1:8081"), "Relay HTTP URL")
	flag.StringVar(&cfg.natsURL, "nats-url", envOrDefault("OPENBOOKING_NATS_URL", "nats://127.0.0.1:4222"), "NATS URL")
	flag.StringVar(&cfg.relayDID, "relay-did", envOrDefault("OPENBOOKING_RELAY_DID", ""), "Relay DID")
	flag.Int64Var(&cfg.readyReplayLagThreshold, "ready-replay-lag-threshold", envOrDefaultInt64("OPENBOOKING_READY_REPLAY_LAG_THRESHOLD", 100), "maximum replay lag before unready")
	flag.Parse()
	if strings.TrimSpace(cfg.postgresDSN) == "" || strings.TrimSpace(cfg.relayDID) == "" {
		slog.Error("missing required appview config")
		os.Exit(1)
	}
	return cfg
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envOrDefaultInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func consumeRelayEvents(ctx context.Context, service *appview.Service, meshClient *mesh.JetStreamMesh, relayDID string) error {
	sub, err := meshClient.JetStream.PullSubscribe(protocol.RelayCommitSubject(relayDID), "appview-"+sanitizeDID(relayDID))
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		msgs, err := sub.Fetch(1, nats.MaxWait(250*time.Millisecond))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			return err
		}
		for _, msg := range msgs {
			var envelope protocol.RelayCommitEnvelope
			if err := json.Unmarshal(msg.Data, &envelope); err != nil {
				msg.Nak()
				return err
			}
			if err := service.ApplyLiveEvent(ctx, envelope); err != nil {
				msg.Nak()
				return err
			}
			msg.Ack()
		}
	}
}

func sanitizeDID(did string) string {
	replacer := strings.NewReplacer(":", "_", "/", "_", ".", "_", "%", "_")
	return replacer.Replace(did)
}

func monitorAppViewDependencies(ctx context.Context, registry *runtimex.Registry, service *appview.Service, store *appviewpostgres.Store, meshClient *mesh.JetStreamMesh) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := store.Ping(checkCtx)
		registry.SetComponent("postgres", err == nil, errorString(err))
		registry.SetComponent("nats", meshClient.Connection != nil && meshClient.Connection.Status() == nats.CONNECTED, "")
		latest, latestErr := service.LatestRelaySequence(checkCtx)
		checkpoint, checkpointErr := service.Checkpoint(checkCtx)
		if latestErr == nil && checkpointErr == nil {
			registry.SetGauge("appview_replay_lag", int64(latest-checkpoint))
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
