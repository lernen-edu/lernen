// Package caching wraps any docs.Provider with a SQLite-backed cache
// for QueryDocs results. ResolveLibrary and HealthCheck pass through
// unchanged: the first because library-id mappings can drift over time
// (Context7 reindexing) and a stale id is a worse failure than a slow
// resolve, the second because a cached health is a non-health.
//
// Cache key: (library_id, topic, max_tokens_bucket). max_tokens is
// bucketed (default {1024,2048,4096,8192,16384,32768}) so that nearby
// values collapse to the same row instead of cache-missing forever.
//
// Default TTL is 30 days per BUILD_ORDER M2. The TTL is checked on read;
// expired rows are refetched and overwritten via UPSERT, never deleted
// proactively (no GC sweep — cache size is bounded by distinct queries
// the user makes; M3 dogfood reveals if a sweep is actually needed).
//
// Driver: modernc.org/sqlite (pure-Go). Preserves cgo-free cross-compile.
//
// Path: defaults to os.UserCacheDir()/lernen/docs_cache.sqlite. Per-OS
// native (Linux ~/.cache, Mac ~/Library/Caches, Windows %LocalAppData%).
package caching

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lernen-edu/lernen/internal/docs"
)

const (
	defaultTTL = 30 * 24 * time.Hour

	schema = `
CREATE TABLE IF NOT EXISTS query_cache (
  library_id        TEXT    NOT NULL,
  topic             TEXT    NOT NULL,
  max_tokens_bucket INTEGER NOT NULL,
  body              TEXT    NOT NULL,
  fetched_at        INTEGER NOT NULL,
  PRIMARY KEY (library_id, topic, max_tokens_bucket)
);
CREATE INDEX IF NOT EXISTS idx_query_cache_fetched_at ON query_cache(fetched_at);
`
)

var defaultBuckets = []int{1024, 2048, 4096, 8192, 16384, 32768}

// Options configures a caching Provider. Zero values are replaced with
// sensible defaults.
type Options struct {
	// DBPath is the SQLite file location. Default
	// os.UserCacheDir()/lernen/docs_cache.sqlite. Parent dirs are created.
	DBPath string

	// TTL is the cache entry lifetime. Default 30 days.
	TTL time.Duration

	// Now is the clock source. Default time.Now. Tests override to drive
	// TTL boundaries.
	Now func() time.Time

	// MaxTokensBucket maps a maxTokens value to a bucket integer used in
	// the cache key. Default rounds up to the smallest of
	// {1024,2048,4096,8192,16384,32768}, then 65536 above that.
	MaxTokensBucket func(int) int
}

// Provider wraps an inner docs.Provider with a SQLite cache.
type Provider struct {
	inner  docs.Provider
	db     *sql.DB
	ttl    time.Duration
	now    func() time.Time
	bucket func(int) int
}

var _ docs.Provider = (*Provider)(nil)

// New opens (or creates) the SQLite cache file at opts.DBPath, applies
// defaults, and returns a Provider wrapping inner.
func New(inner docs.Provider, opts Options) (*Provider, error) {
	if opts.DBPath == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("caching: locate user cache dir: %w", err)
		}
		opts.DBPath = filepath.Join(cacheDir, "lernen", "docs_cache.sqlite")
	}
	if opts.TTL <= 0 {
		opts.TTL = defaultTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxTokensBucket == nil {
		opts.MaxTokensBucket = defaultBucket
	}

	if err := os.MkdirAll(filepath.Dir(opts.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("caching: create cache dir: %w", err)
	}

	db, err := sql.Open("sqlite", opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("caching: open db: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("caching: create schema: %w", err)
	}

	return &Provider{
		inner:  inner,
		db:     db,
		ttl:    opts.TTL,
		now:    opts.Now,
		bucket: opts.MaxTokensBucket,
	}, nil
}

// Close releases the underlying database handle. Safe to call multiple
// times; subsequent QueryDocs calls return an error rather than panic.
func (p *Provider) Close() error {
	return p.db.Close()
}

// ResolveLibrary passes through to the inner provider unchanged. We do
// not cache library-name lookups because Context7's index can shift the
// best-match for a name and a stale id is a worse failure than a slow
// resolve.
func (p *Provider) ResolveLibrary(ctx context.Context, name string) (docs.LibraryID, error) {
	return p.inner.ResolveLibrary(ctx, name)
}

// HealthCheck passes through to the inner provider unchanged. A cached
// health is a non-health.
func (p *Provider) HealthCheck(ctx context.Context) error {
	return p.inner.HealthCheck(ctx)
}

// QueryDocs returns a cached body when one is present and unexpired,
// otherwise calls the inner provider and stores the result. A failed
// inner call is propagated and not cached.
func (p *Provider) QueryDocs(ctx context.Context, lib docs.LibraryID, topic string, maxTokens int) (string, error) {
	bucket := p.bucket(maxTokens)
	now := p.now()

	var cachedBody string
	var fetchedAt int64
	err := p.db.QueryRowContext(ctx,
		`SELECT body, fetched_at FROM query_cache WHERE library_id = ? AND topic = ? AND max_tokens_bucket = ?`,
		string(lib), topic, bucket,
	).Scan(&cachedBody, &fetchedAt)

	switch {
	case err == nil:
		if now.Sub(time.Unix(fetchedAt, 0)) < p.ttl {
			return cachedBody, nil
		}
		// Expired — fall through to refetch.
	case errors.Is(err, sql.ErrNoRows):
		// Cache miss — fall through to fetch.
	default:
		return "", fmt.Errorf("caching: query cache: %w", err)
	}

	body, err := p.inner.QueryDocs(ctx, lib, topic, maxTokens)
	if err != nil {
		return "", err
	}

	// UPSERT to handle both "first insert" and "expired refetch" without
	// a separate code path. A failed cache write does not fail the user-
	// facing call: they already have the doc; degrading to no-cache is
	// better than failing their tutor turn.
	_, _ = p.db.ExecContext(ctx,
		`INSERT INTO query_cache (library_id, topic, max_tokens_bucket, body, fetched_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (library_id, topic, max_tokens_bucket) DO UPDATE SET
		   body = excluded.body, fetched_at = excluded.fetched_at`,
		string(lib), topic, bucket, body, now.Unix(),
	)

	return body, nil
}

func defaultBucket(maxTokens int) int {
	if maxTokens <= 0 {
		return 4096
	}
	for _, b := range defaultBuckets {
		if maxTokens <= b {
			return b
		}
	}
	return 65536
}
