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

	"github.com/restayway/openbooking/pkg/auth"
	"github.com/restayway/openbooking/pkg/did"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/mesh"
	"github.com/restayway/openbooking/pkg/openbao"
	"github.com/restayway/openbooking/pkg/pds"
	"github.com/restayway/openbooking/pkg/protocol"
	"github.com/restayway/openbooking/pkg/runtimex"
	postgresstore "github.com/restayway/openbooking/pkg/store/postgres"
)

func main() {
	cfg := loadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	registry := runtimex.NewRegistry()

	store, err := postgresstore.Open(cfg.postgresDSN)
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

	if err := meshClient.EnsureStream("OPENBOOKING_REPO", "OBP.REPO.*.COMMIT"); err != nil {
		logger.Error("ensure repo stream", "error", err)
		os.Exit(1)
	}
	if err := meshClient.EnsureStream("OPENBOOKING_INTENT", "OBP.INTENT.*"); err != nil {
		logger.Error("ensure intent stream", "error", err)
		os.Exit(1)
	}
	transitClient := openbao.TransitClient{Address: cfg.openbaoAddr, Token: cfg.openbaoToken}

	resolver := did.NewCachingResolver(did.DefaultMethodResolver(nil), 5*time.Minute, 30*time.Second)

	service, err := pds.NewService(ctx, pds.Config{
		ActorDID:        cfg.actorDID,
		Role:            pds.Role(cfg.actorRole),
		KeyID:           cfg.openbaoKeyID,
		EncryptionKeyID: cfg.openbaoPrivateKeyID,
	}, store, jetstreamPublisher{mesh: meshClient}, openbaoSigner{client: transitClient}, openbaoCipher{client: transitClient}, resolver)
	if err != nil {
		logger.Error("initialize service", "error", err)
		os.Exit(1)
	}

	authVerifier := auth.NewVerifier(resolver, time.Minute)

	server := &http.Server{
		Addr: cfg.httpAddr,
		Handler: runtimex.ComposeHandler(pds.NewHandlerWithAuth(service, authVerifier), registry, func(snapshot runtimex.Snapshot) (bool, map[string]any) {
			backlog := snapshot.Gauges["pds_outbox_backlog"]
			depsOK := snapshot.Components["postgres"].Healthy && snapshot.Components["nats"].Healthy && snapshot.Components["openbao"].Healthy && snapshot.Components["did"].Healthy
			ready := depsOK && snapshot.Components["outbox_worker"].Healthy && backlog <= cfg.readyOutboxThreshold
			return ready, map[string]any{
				"ready":         ready,
				"outboxBacklog": backlog,
				"components":    snapshot.Components,
			}
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		runtimex.RunRetriable(ctx, logger, registry, "outbox_worker", runtimex.RetryConfig{InitialBackoff: time.Second, MaxBackoff: 15 * time.Second}, func(ctx context.Context) error {
			return pds.RunOutboxPublisher(ctx, time.Second, service)
		})
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if service.Config().Role == pds.RoleProvider {
		go func() {
			runtimex.RunRetriable(ctx, logger, registry, "intent_consumer", runtimex.RetryConfig{InitialBackoff: time.Second, MaxBackoff: 15 * time.Second}, func(ctx context.Context) error {
				return consumeProviderIntents(ctx, service, meshClient)
			})
		}()
	}
	go monitorPDSDependencies(ctx, registry, store, meshClient, transitClient, cfg.openbaoKeyID, cfg.actorDID, resolver)

	logger.Info("openbooking-pds listening", "addr", cfg.httpAddr, "actorDid", cfg.actorDID, "role", cfg.actorRole)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}

type runtimeConfig struct {
	actorDID             string
	actorRole            string
	httpAddr             string
	postgresDSN          string
	natsURL              string
	openbaoAddr          string
	openbaoToken         string
	openbaoKeyID         string
	openbaoPrivateKeyID  string
	readyOutboxThreshold int64
}

func loadConfig() runtimeConfig {
	cfg := runtimeConfig{}
	flag.StringVar(&cfg.actorDID, "actor-did", envOrDefault("OPENBOOKING_ACTOR_DID", ""), "local actor DID")
	flag.StringVar(&cfg.actorRole, "actor-role", envOrDefault("OPENBOOKING_ACTOR_ROLE", "provider"), "actor role")
	flag.StringVar(&cfg.httpAddr, "http-addr", envOrDefault("OPENBOOKING_HTTP_ADDR", ":8080"), "HTTP listen address")
	flag.StringVar(&cfg.postgresDSN, "postgres-dsn", envOrDefault("OPENBOOKING_POSTGRES_DSN", ""), "PostgreSQL DSN")
	flag.StringVar(&cfg.natsURL, "nats-url", envOrDefault("OPENBOOKING_NATS_URL", natsDefaultURL), "NATS URL")
	flag.StringVar(&cfg.openbaoAddr, "openbao-addr", envOrDefault("OPENBOOKING_OPENBAO_ADDR", ""), "OpenBao address")
	flag.StringVar(&cfg.openbaoToken, "openbao-token", envOrDefault("OPENBOOKING_OPENBAO_TOKEN", ""), "OpenBao token")
	flag.StringVar(&cfg.openbaoKeyID, "openbao-key-id", envOrDefault("OPENBOOKING_OPENBAO_KEY_ID", ""), "OpenBao transit key ID")
	flag.StringVar(&cfg.openbaoPrivateKeyID, "openbao-private-key-id", envOrDefault("OPENBOOKING_OPENBAO_PRIVATE_KEY_ID", ""), "OpenBao transit encryption key ID")
	flag.Int64Var(&cfg.readyOutboxThreshold, "ready-outbox-threshold", envOrDefaultInt64("OPENBOOKING_READY_OUTBOX_THRESHOLD", 100), "maximum unpublished outbox backlog before unready")
	flag.Parse()

	required := map[string]string{
		"actor-did":      cfg.actorDID,
		"postgres-dsn":   cfg.postgresDSN,
		"openbao-addr":   cfg.openbaoAddr,
		"openbao-token":  cfg.openbaoToken,
		"openbao-key-id": cfg.openbaoKeyID,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			slog.Error("missing required config", "name", name)
			os.Exit(1)
		}
	}
	if strings.TrimSpace(cfg.openbaoPrivateKeyID) == "" {
		cfg.openbaoPrivateKeyID = cfg.openbaoKeyID
	}
	return cfg
}

const natsDefaultURL = "nats://127.0.0.1:4222"

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
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

type jetstreamPublisher struct {
	mesh *mesh.JetStreamMesh
}

func (p jetstreamPublisher) Publish(_ context.Context, subject string, payload []byte) error {
	_, err := p.mesh.JetStream.Publish(subject, payload)
	return err
}

type openbaoSigner struct {
	client openbao.TransitClient
}

func (s openbaoSigner) Sign(ctx context.Context, message []byte, keyID string) ([]byte, error) {
	return s.client.Sign(ctx, message, keyID)
}

type openbaoCipher struct {
	client openbao.TransitClient
}

func (c openbaoCipher) Encrypt(ctx context.Context, plaintext []byte, keyID string) (string, error) {
	return c.client.Encrypt(ctx, plaintext, keyID)
}

func (c openbaoCipher) Decrypt(ctx context.Context, ciphertext, keyID string) ([]byte, error) {
	return c.client.Decrypt(ctx, ciphertext, keyID)
}

func consumeProviderIntents(ctx context.Context, service *pds.Service, meshClient *mesh.JetStreamMesh) error {
	subject := protocol.IntentSubject(service.Config().ActorDID)
	sub, err := meshClient.JetStream.PullSubscribe(subject, "pds-intents-"+sanitizeDID(service.Config().ActorDID))
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
			var intent lexicon.ReservationIntent
			if err := json.Unmarshal(msg.Data, &intent); err != nil {
				msg.Nak()
				return err
			}
			if _, err := service.ProcessIntent(ctx, intent, time.Now().UTC()); err != nil {
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

func monitorPDSDependencies(ctx context.Context, registry *runtimex.Registry, store *postgresstore.Store, meshClient *mesh.JetStreamMesh, transitClient openbao.TransitClient, keyID, actorDID string, resolver did.Resolver) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := store.Ping(checkCtx)
		registry.SetComponent("postgres", err == nil, errorString(err))
		backlog, backlogErr := store.PendingOutboxCount(checkCtx)
		if backlogErr == nil {
			registry.SetGauge("pds_outbox_backlog", backlog)
		}
		registry.SetComponent("nats", meshClient.Connection != nil && meshClient.Connection.Status() == nats.CONNECTED, "")
		err = transitClient.Preflight(checkCtx, keyID)
		registry.SetComponent("openbao", err == nil, errorString(err))
		doc, err := resolver.Resolve(checkCtx, actorDID)
		if err == nil {
			err = did.HasVerificationMethod(doc, keyID)
		}
		registry.SetComponent("did", err == nil, errorString(err))
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
