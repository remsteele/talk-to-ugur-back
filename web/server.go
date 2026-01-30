package web

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"talk-to-ugur-back/ai"
	"talk-to-ugur-back/config"
	"talk-to-ugur-back/models"
	"talk-to-ugur-back/models/db"
)

type Server struct {
	dbQueries *db.Queries
	pgPool    *pgxpool.Pool
	cfg       *config.Config
	aiClient  *ai.Client
	startTime time.Time
	ready     atomic.Bool
}

func NewServer(ctx context.Context) (*Server, error) {
	cfg, err := config.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}

	pgConfig, err := pgxpool.ParseConfig(cfg.PostgresConnString)
	if err != nil {
		return nil, err
	}

	pgPool, err := pgxpool.NewWithConfig(ctx, pgConfig)
	if err != nil {
		return nil, err
	}

	for {
		if err = pgPool.Ping(ctx); err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		fmt.Printf("Error connecting to database: %s\nTrying again in 5 seconds...\n", err.Error())
		select {
		case <-time.After(5 * time.Second):
			continue
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if err = models.Migrate(cfg.PostgresConnString); err != nil {
		return nil, err
	}

	queries := db.New(pgPool)
	aiClient := ai.NewClient(cfg)

	server := &Server{
		dbQueries: queries,
		pgPool:    pgPool,
		cfg:       cfg,
		aiClient:  aiClient,
		startTime: time.Now(),
	}
	return server, nil
}

func (s *Server) Run(ctx context.Context) error {
	eng := s.makeRoutes()
	listener, err := net.Listen("tcp", s.cfg.HTTPListenAddr)
	if err != nil {
		return err
	}

	s.ready.Store(true)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	return eng.RunListener(listener)
}
