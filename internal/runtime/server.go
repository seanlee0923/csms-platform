package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"github.com/seanlee0923/csms-platform/internal/commandbus"
	"github.com/seanlee0923/csms-platform/internal/commandbus/redisbus"
	"github.com/seanlee0923/csms-platform/internal/handlers"
	"github.com/seanlee0923/csms-platform/internal/sessionregistry/redisstore"
	"github.com/seanlee0923/csms-platform/internal/stationstore"
	"github.com/seanlee0923/csms-platform/internal/stationstore/mysqlstore"
	"github.com/seanlee0923/ocpp/csms"
)

type Server struct {
	config       Config
	logger       *slog.Logger
	ocpp         *csms.Server
	http         *http.Server
	health       *health
	shutdownOCPP func(context.Context) error
	shutdownHTTP func(context.Context) error
	store        *stationstore.Memory
	database     *sql.DB
	redis        *redis.Client
	ownership    *ownershipManager
	profiles     *handlers.Profiles
	commandBus   commandbus.Bus
	tlsConfig    *tls.Config
}

// buildTLSConfig returns nil, nil if config.TLSCertFile/TLSKeyFile do not
// exist on disk — TLS is off by default and activates only when the
// Operator (or an operator running this Runtime outside Kubernetes) has
// actually placed a certificate at that path, matching CSMS.spec.tls's
// volume mount convention. It never generates or fetches a certificate
// itself.
func buildTLSConfig(config Config) (*tls.Config, error) {
	certExists := fileExists(config.TLSCertFile)
	keyExists := fileExists(config.TLSKeyFile)
	if !certExists && !keyExists {
		return nil, nil
	}
	if certExists != keyExists {
		return nil, fmt.Errorf("TLS requires both a certificate and a key file: cert=%s (exists=%t) key=%s (exists=%t)",
			config.TLSCertFile, certExists, config.TLSKeyFile, keyExists)
	}
	certificate, err := tls.LoadX509KeyPair(config.TLSCertFile, config.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	if fileExists(config.TLSClientCAFile) {
		caBytes, err := os.ReadFile(config.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("no valid certificates found in TLS client CA file %s", config.TLSClientCAFile)
		}
		tlsConfig.ClientCAs = pool
		// VerifyClientCertIfGiven, not RequireAndVerifyClientCert: kubelet's
		// HTTPGet liveness/readiness probes never present a client
		// certificate, so requiring one at the TLS handshake layer would
		// permanently fail health checks and crash-loop the Pod. A verified
		// certificate is still required to reach the OCPP path — see the
		// Authenticator wired into ocppConfig.Security below, which runs
		// only for the OCPP WebSocket upgrade, not for /livez/readyz/metrics.
		tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return tlsConfig, nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func New(config Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	tlsConfig, err := buildTLSConfig(config)
	if err != nil {
		return nil, err
	}
	memoryStore := stationstore.NewMemory()
	var repository stationstore.Repository = memoryStore
	var database *sql.DB
	if config.MySQLDSN != "" {
		var err error
		database, err = sql.Open("mysql", config.MySQLDSN)
		if err != nil {
			return nil, fmt.Errorf("open mysql: %w", err)
		}
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := database.PingContext(connectCtx); err != nil {
			database.Close()
			return nil, fmt.Errorf("ping mysql: %w", err)
		}
		if err := mysqlstore.Migrate(connectCtx, database); err != nil {
			database.Close()
			return nil, err
		}
		repository, err = mysqlstore.New(database)
		if err != nil {
			database.Close()
			return nil, err
		}
		memoryStore = nil
	}
	router := csms.NewRouter()
	profiles, err := handlers.Register(router, config.HeartbeatInterval, logger, repository)
	if err != nil {
		if database != nil {
			database.Close()
		}
		return nil, err
	}
	var redisClient *redis.Client
	var ownership *ownershipManager
	var commands commandbus.Bus
	var rateLimiter commandRateLimiter
	if config.RedisURL != "" {
		options, err := redis.ParseURL(config.RedisURL)
		if err != nil {
			if database != nil {
				database.Close()
			}
			return nil, fmt.Errorf("parse redis URL: %w", err)
		}
		redisClient = redis.NewClient(options)
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := redisClient.Ping(connectCtx).Err(); err != nil {
			redisClient.Close()
			if database != nil {
				database.Close()
			}
			return nil, fmt.Errorf("ping redis: %w", err)
		}
		registry, err := redisstore.New(redisClient, "csms")
		if err != nil {
			redisClient.Close()
			if database != nil {
				database.Close()
			}
			return nil, err
		}
		instanceID := config.InstanceID
		if instanceID == "" {
			instanceID, err = os.Hostname()
			if err != nil {
				redisClient.Close()
				if database != nil {
					database.Close()
				}
				return nil, fmt.Errorf("resolve runtime instance ID: %w", err)
			}
		}
		ownership = newOwnershipManager(registry, instanceID, config.SessionLeaseTTL, config.SessionRenew, logger)
		commands, err = redisbus.New(redisClient, "csms", 5*time.Minute)
		if err != nil {
			redisClient.Close()
			if database != nil {
				database.Close()
			}
			return nil, err
		}
		rateLimiter = &redisCommandRateLimiter{
			client: redisClient, prefix: "csms", limit: int64(config.CommandRateLimit), window: time.Minute,
		}
	}
	var ocppServer *csms.Server
	metrics := newRuntimeMetrics(func() int {
		if ocppServer == nil {
			return 0
		}
		return ocppServer.SessionCount()
	})
	ocppConfig := csms.Config{
		Router: router, Versions: config.Versions,
		Logger: csms.LoggerFunc(func(ctx context.Context, record csms.LogRecord) {
			logger.Log(ctx, ocppLogLevel(record.Level), string(record.Event),
				"identity", record.Identity,
				"version", record.Version,
				"message_type", record.MessageType,
				"message_id", record.MessageID,
				"action", record.Action,
				"error_code", record.ErrorCode,
				"reason", record.Reason,
			)
		}),
		Metrics: csms.MetricsFunc(metrics.recordOCPPEvent),
	}
	if ownership != nil {
		ocppConfig.OnConnect = ownership.onConnect
		ocppConfig.OnDisconnect = ownership.onDisconnect
	}
	if tlsConfig != nil && tlsConfig.ClientAuth == tls.VerifyClientCertIfGiven {
		// The TLS layer verifies a client certificate if one is presented
		// but does not require it (see buildTLSConfig), so
		// SecurityProfileTLSClientCertificate is what actually rejects an
		// OCPP WebSocket upgrade with no certificate at all; this
		// Authenticator adds the further check that a presented
		// certificate actually belongs to the station connecting under
		// this identity, not just any station trusted by the CA.
		ocppConfig.Security = csms.SecurityConfig{
			Profile:       csms.SecurityProfileTLSClientCertificate,
			MinTLSVersion: tls.VersionTLS12,
			Authenticator: csms.AuthenticatorFunc(func(_ context.Context, request csms.AuthenticationRequest) (csms.Principal, error) {
				if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
					return csms.Principal{}, fmt.Errorf("client certificate is required")
				}
				commonName := request.TLS.PeerCertificates[0].Subject.CommonName
				if commonName != request.Identity {
					return csms.Principal{}, fmt.Errorf("client certificate CN %q does not match station identity %q", commonName, request.Identity)
				}
				return csms.Principal{ID: request.Identity}, nil
			}),
		}
	}
	ocppServer, err = csms.New(ocppConfig)
	if err != nil {
		if redisClient != nil {
			redisClient.Close()
		}
		if database != nil {
			database.Close()
		}
		return nil, err
	}
	h := &health{}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", h.live)
	mux.HandleFunc("/readyz", h.readiness)
	mux.HandleFunc("/metrics", metrics.serveHTTP)
	mux.Handle("/api/v1/stations/", metrics.commandMiddleware(
		serverCommandHandler(config.APIKeys, ownership, commands, rateLimiter, logger),
	))
	mux.Handle("/", ocppServer)
	server := &Server{
		config: config, logger: logger, ocpp: ocppServer, health: h, store: memoryStore, database: database,
		redis: redisClient, ownership: ownership,
		profiles: profiles, commandBus: commands, tlsConfig: tlsConfig,
		http: &http.Server{Addr: config.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, TLSConfig: tlsConfig},
	}
	if tlsConfig != nil {
		logger.Info("TLS enabled", "mutual_tls", tlsConfig.ClientAuth == tls.VerifyClientCertIfGiven)
	}
	server.shutdownOCPP = server.ocpp.Shutdown
	server.shutdownHTTP = server.http.Shutdown
	return server, nil
}

func ocppLogLevel(level csms.LogLevel) slog.Level {
	switch level {
	case csms.LogDebug:
		return slog.LevelDebug
	case csms.LogWarn:
		return slog.LevelWarn
	case csms.LogError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.HTTPAddr)
	if err != nil {
		if s.database != nil {
			s.database.Close()
		}
		if s.redis != nil {
			s.redis.Close()
		}
		return err
	}
	return s.serve(ctx, listener)
}

func (s *Server) serve(ctx context.Context, listener net.Listener) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	s.health.ready.Store(true)
	s.logger.Info("CSMS runtime listening", "address", listener.Addr())
	errCh := make(chan error, 1)
	go func() {
		if s.tlsConfig != nil {
			// Certificates are already loaded into http.Server.TLSConfig by
			// New(), so no cert/key file paths are needed here.
			errCh <- s.http.ServeTLS(listener, "", "")
			return
		}
		errCh <- s.http.Serve(listener)
	}()
	if s.commandBus != nil {
		go s.runCommandConsumer(runCtx)
	}
	select {
	case err := <-errCh:
		s.health.ready.Store(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.health.ready.Store(false)
		cancelRun()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		ocppErr := s.shutdownOCPP(shutdownCtx)
		httpErr := s.shutdownHTTP(shutdownCtx)
		var databaseErr error
		if s.database != nil {
			databaseErr = s.database.Close()
		}
		var redisErr error
		if s.redis != nil {
			redisErr = s.redis.Close()
		}
		return errors.Join(ocppErr, httpErr, databaseErr, redisErr)
	}
}

func (s *Server) runCommandConsumer(ctx context.Context) {
	for {
		err := s.commandBus.RunConsumer(ctx, s.ownership.ownerID, s.handleCommand)
		if ctx.Err() != nil {
			return
		}
		s.health.ready.Store(false)
		s.logger.Error("command consumer unavailable", "error", err)
		for {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			pingErr := s.redis.Ping(pingCtx).Err()
			cancel()
			if pingErr == nil {
				s.health.ready.Store(true)
				s.logger.Info("command consumer Redis connection recovered")
				break
			}
			s.logger.Warn("command consumer Redis reconnect pending", "error", pingErr)
		}
	}
}
