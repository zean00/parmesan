package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sahal/parmesan/internal/secrets"
)

type Client struct {
	Pool    *pgxpool.Pool
	Crypter *secrets.Crypter
}

func Connect(ctx context.Context, databaseURL string) (*Client, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Client{Pool: pool}, nil
}

func (c *Client) Close() {
	if c != nil && c.Pool != nil {
		c.Pool.Close()
	}
}
