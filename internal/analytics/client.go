// internal/analytics/client.go
/*
Copyright 2026 Claudio Botelho.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package analytics

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	driver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// ClickHouseConfig holds the parameters required to open a ClickHouse connection.
type ClickHouseConfig struct {
	// Endpoint is the host:port of the ClickHouse server (e.g. "ch.internal:9000").
	Endpoint string

	// Username and Password are the ClickHouse credentials.
	// Sourced from a Kubernetes Secret at runtime.
	Username string
	Password string

	// Database is the target database; defaults to "taoguardian".
	Database string

	// Timeouts and pool sizing. Zero values use safe defaults.
	DialTimeout     time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration

	// TLSEnabled enables TLS for the connection (required for managed ClickHouse).
	TLSEnabled bool
}

// NewClickHouseConn opens and validates a ClickHouse native-protocol connection.
// It pings the server before returning to fail fast on misconfiguration.
func NewClickHouseConn(cfg ClickHouseConfig) (driver.Conn, error) {
	if cfg.Database == "" {
		cfg.Database = "taoguardian"
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = 5
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 2
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = time.Hour
	}

	opts := &clickhouse.Options{
		Addr: []string{cfg.Endpoint},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		DialTimeout:     cfg.DialTimeout,
		MaxOpenConns:    cfg.MaxOpenConns,
		MaxIdleConns:    cfg.MaxIdleConns,
		ConnMaxLifetime: cfg.ConnMaxLifetime,
		Settings: clickhouse.Settings{
			"max_execution_time": 30,
		},
		Compression: &clickhouse.Compression{
			Method: clickhouse.CompressionLZ4,
		},
	}

	if cfg.TLSEnabled {
		opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse connection to %s: %w", cfg.Endpoint, err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("ping clickhouse at %s: %w", cfg.Endpoint, err)
	}

	return conn, nil
}
