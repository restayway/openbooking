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
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/restayway/openbooking/pkg/did"
	"github.com/restayway/openbooking/pkg/mesh"
	"github.com/restayway/openbooking/pkg/protocol"
	"github.com/restayway/openbooking/pkg/relay"
	"github.com/restayway/openbooking/pkg/runtimex"
	relaypostgres "github.com/restayway/openbooking/pkg/store/relaypostgres"
)

func main() {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	registry := runtimex.NewRegistry()

	store, err := relaypostgres.Open(cfg.postgresDSN)
	if err != nil {
		logger.Error("open postgres", "error", err)
		os.Exit(1)
	}
	defer store.Close()

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

	service, err := relay.NewService(ctx, cfg.relayDID, store, relayPublisher{mesh: meshClient}, did.WebResolver{})
	if err != nil {
		logger.Error("init relay service", "error", err)
		os.Exit(1)
	}

	server := &http.Server{Addr: cfg.httpAddr, Handler: runtimex.ComposeHandler(relay.NewHandler(service), registry, func(snapshot runtimex.Snapshot) (bool, map[string]any) {
		ready := snapshot.Components["postgres"].Healthy && snapshot.Components["nats"].Healthy && snapshot.Components["repo_consumer"].Healthy
		return ready, map[string]any{"ready": ready, "components": snapshot.Components}
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		runtimex.RunRetriable(ctx, logger, registry, "repo_consumer", runtimex.RetryConfig{InitialBackoff: time.Second, MaxBackoff: 15 * time.Second}, func(ctx context.Context) error {
			return consumeRepoCommits(ctx, service, meshClient, cfg.relayDID)
		})
	}()
	go monitorRelayDependencies(ctx, registry, store, meshClient)

	logger.Info("openbooking-relay listening", "addr", cfg.httpAddr, "relayDid", cfg.relayDID)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("relay http server failed", "error", err)
		os.Exit(1)
	}
}

type config struct {
	relayDID    string
	httpAddr    string
	postgresDSN string
	natsURL     string
}

func loadConfig() config {
	cfg := config{}
	flag.StringVar(&cfg.relayDID, "relay-did", envOrDefault("OPENBOOKING_RELAY_DID", ""), "relay DID")
	flag.StringVar(&cfg.httpAddr, "http-addr", envOrDefault("OPENBOOKING_RELAY_HTTP_ADDR", ":8081"), "HTTP listen address")
	flag.StringVar(&cfg.postgresDSN, "postgres-dsn", envOrDefault("OPENBOOKING_RELAY_POSTGRES_DSN", ""), "PostgreSQL DSN")
	flag.StringVar(&cfg.natsURL, "nats-url", envOrDefault("OPENBOOKING_NATS_URL", "nats://127.0.0.1:4222"), "NATS URL")
	flag.Parse()
	if strings.TrimSpace(cfg.relayDID) == "" || strings.TrimSpace(cfg.postgresDSN) == "" {
		slog.Error("missing required relay config")
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

type relayPublisher struct{ mesh *mesh.JetStreamMesh }

func (p relayPublisher) Publish(_ context.Context, subject string, payload []byte) error {
	_, err := p.mesh.JetStream.Publish(subject, payload)
	return err
}

func consumeRepoCommits(ctx context.Context, service *relay.Service, meshClient *mesh.JetStreamMesh, relayDID string) error {
	consumer := "relay-" + sanitizeDID(relayDID)
	sub, err := meshClient.JetStream.PullSubscribe("OBP.REPO.*.COMMIT", consumer)
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
			var commit protocol.Commit
			if err := json.Unmarshal(msg.Data, &commit); err != nil {
				msg.Nak()
				return err
			}
			meta, _ := msg.Metadata()
			sourceSequence := uint64(0)
			if meta != nil {
				sourceSequence = meta.Sequence.Stream
			}
			if _, err := service.IngestCommit(ctx, commit, msg.Subject, sourceSequence, time.Now().UTC()); err != nil {
				msg.Nak()
				return err
			}
			if err := service.SetConsumerCheckpoint(ctx, consumer, sourceSequence); err != nil {
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

func monitorRelayDependencies(ctx context.Context, registry *runtimex.Registry, store *relaypostgres.Store, meshClient *mesh.JetStreamMesh) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := store.Ping(checkCtx)
		registry.SetComponent("postgres", err == nil, errorString(err))
		registry.SetComponent("nats", meshClient.Connection != nil && meshClient.Connection.Status() == nats.CONNECTED, "")
		sequence, err := store.LastRelaySequence(checkCtx)
		if err == nil {
			registry.SetGauge("relay_latest_sequence", int64(sequence))
		}
		source, err := store.LastConsumerCheckpoint(checkCtx)
		if err == nil {
			registry.SetGauge("relay_last_source_sequence", int64(source))
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
