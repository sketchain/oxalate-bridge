// Package cpaquota reads the tables the cpa-quota plugin keeps in the shared
// PostgreSQL database (schema BRIDGE_CPA_QUOTA_SCHEMA). Access is read-only:
// every query runs in a READ ONLY transaction with a short statement timeout
// and the cpa-quota schema as its search path. The table definitions in
// schema.sql are a reference copy for sqlc, not migrations of this project.
package cpaquota

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota/cqsql"
)

// Query limits.
const (
	statementTimeout = 3 * time.Second
	callTimeout      = 5 * time.Second
)

// ErrUnavailable means the cpa-quota schema or table is missing, or the
// query did not finish in time.
var ErrUnavailable = errors.New("cpa-quota data unavailable")

// ErrNotFound means cpa-quota has no row for the account.
var ErrNotFound = errors.New("not found in cpa-quota")

// Reader runs read-only queries against the cpa-quota schema.
type Reader struct {
	Pool   *pgxpool.Pool
	Schema string
}

// Read runs fn in a read-only transaction on the cpa-quota schema.
func (r *Reader) Read(ctx context.Context, fn func(ctx context.Context, q *cqsql.Queries) error) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return classify(ctx, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	setup := fmt.Sprintf("SET LOCAL search_path TO %s; SET LOCAL statement_timeout = %d; SET LOCAL TimeZone = 'UTC'",
		pgx.Identifier{r.Schema}.Sanitize(), statementTimeout.Milliseconds())
	if _, err := tx.Exec(ctx, setup); err != nil {
		return classify(ctx, err)
	}
	if err := fn(ctx, cqsql.New(tx)); err != nil {
		return classify(ctx, err)
	}
	return nil
}

func classify(ctx context.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "3F000", // invalid_schema_name
			"42P01", // undefined_table
			"42703", // undefined_column (an incompatible cpa-quota schema version)
			"57014": // query_canceled (statement timeout)
			return fmt.Errorf("%w: %s", ErrUnavailable, pgErr.Message)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return fmt.Errorf("%w: query timed out", ErrUnavailable)
	}
	return err
}

// NsOf converts a time to Unix nanoseconds (the cpa-quota time unit).
func NsOf(t time.Time) int64 { return t.UnixNano() }

// TimeOf converts Unix nanoseconds to a time; 0 means unknown.
func TimeOf(ns int64) *time.Time {
	if ns == 0 {
		return nil
	}
	t := time.Unix(0, ns).UTC()
	return &t
}

// MaxPoints is the most points a quota series returns before downsampling.
const MaxPoints = 2000

// Point is one reported window utilization.
type Point struct {
	At          time.Time
	UsedPercent float64
	ResetAt     *time.Time
}

// Series is a window's utilization over [From, To).
type Series struct {
	Total       int64
	Downsampled bool
	BucketWidth time.Duration
	Points      []Point
}

// WindowSeries returns the unified window of the given length (in seconds)
// for an account, downsampled to MaxPoints buckets (last point of each)
// when the range holds more points.
func (r *Reader) WindowSeries(ctx context.Context, instance, authIndex string, seconds int64, from, to time.Time) (Series, error) {
	var out Series
	fromNs, toNs := NsOf(from), NsOf(to)
	err := r.Read(ctx, func(ctx context.Context, q *cqsql.Queries) error {
		n, err := q.CountWindowPoints(ctx, cqsql.CountWindowPointsParams{Instance: instance, AuthIndex: authIndex,
			Seconds: seconds, FromNs: fromNs, ToNs: toNs})
		if err != nil {
			return err
		}
		out.Total = n
		type row struct {
			at, reset int64
			used      float64
		}
		var rows []row
		if n <= MaxPoints {
			res, err := q.ListWindowPoints(ctx, cqsql.ListWindowPointsParams{Instance: instance, AuthIndex: authIndex,
				Seconds: seconds, FromNs: fromNs, ToNs: toNs})
			if err != nil {
				return err
			}
			for _, p := range res {
				rows = append(rows, row{p.AtNs, p.ResetNs, p.UsedPercent})
			}
		} else {
			width := BucketWidth(fromNs, toNs, MaxPoints)
			out.Downsampled, out.BucketWidth = true, time.Duration(width)
			res, err := q.ListWindowPointsBucketed(ctx, cqsql.ListWindowPointsBucketedParams{Instance: instance, AuthIndex: authIndex,
				Seconds: seconds, FromNs: fromNs, ToNs: toNs, WidthNs: width})
			if err != nil {
				return err
			}
			for _, p := range res {
				rows = append(rows, row{p.AtNs, p.ResetNs, p.UsedPercent})
			}
		}
		out.Points = make([]Point, len(rows))
		for i, p := range rows {
			out.Points[i] = Point{At: *TimeOf(p.at), UsedPercent: p.used, ResetAt: TimeOf(p.reset)}
		}
		return nil
	})
	return out, err
}

// BucketWidth is the width (ns) of n equal buckets covering [from, to),
// rounded up so that at most n buckets exist.
func BucketWidth(fromNs, toNs int64, n int64) int64 {
	span := toNs - fromNs
	if span <= 0 || n <= 0 {
		return 1
	}
	w := span / n
	if span%n != 0 {
		w++
	}
	return max(w, 1)
}
