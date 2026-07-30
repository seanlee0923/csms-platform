package runtime

import (
	"context"
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
}

func New(config Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
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
		profiles: profiles, commandBus: commands,
		http: &http.Server{Addr: config.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second},
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
	go func() { errCh <- s.http.Serve(listener) }()
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
