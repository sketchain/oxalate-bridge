// Command cpa-quota-migrate is copied into a checkout of the pinned
// cpa-quota release by the Check workflow. It migrates a fresh schema with
// the plugin's own Migrate, so TestUpstreamSchemaDrift can compare it with
// the bridge's reference copy (backend/internal/cpaquota/schema.sql).
//
// Usage (from the cpa-quota checkout): go run ./drift-migrate <dsn> <schema>
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/sketchain/cpa-quota/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: cpa-quota-migrate <dsn> <schema>")
	}
	cfg, err := pgx.ParseConfig(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := store.Migrate(ctx, db, os.Args[2]); err != nil {
		log.Fatal(err)
	}
	log.Printf("migrated schema %s to cpa-quota schema version %d", os.Args[2], store.SchemaVersion)
}
