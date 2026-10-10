// Command seed builds the databases that ship inside the images.
//
// It runs at IMAGE BUILD TIME, not at boot. That is what lets userd and
// productsd scale horizontally despite using an embedded database: every task
// ships a byte-identical file, so all tasks answer reads the same.
//
// Consequence, stated plainly: writes to those two services land on one task
// and do not survive task replacement. Anything run at more than one task must
// use seeded data.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"gorm.io/gorm"

	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/store"
	"github.com/lakhansamani/grpc-ecs-demo/internal/product"
	"github.com/lakhansamani/grpc-ecs-demo/internal/seeddata"
	"github.com/lakhansamani/grpc-ecs-demo/internal/user"
)

func main() {
	userDB := flag.String("user-db", "", "sqlite URL for the user database")
	productDB := flag.String("product-db", "", "sqlite URL for the product database")
	flag.Parse()

	if *userDB == "" && *productDB == "" {
		log.Fatal("seed: give -user-db and/or -product-db")
	}
	if *userDB != "" {
		if err := seedUsers(*userDB); err != nil {
			log.Fatalf("seed users: %v", err)
		}
	}
	if *productDB != "" {
		if err := seedProducts(*productDB); err != nil {
			log.Fatalf("seed products: %v", err)
		}
	}
}

func seedUsers(dbURL string) error {
	db, err := store.Open(store.Config{Driver: "sqlite", URL: dbURL}, user.Models()...)
	if err != nil {
		return err
	}
	n, err := seeddata.LoadUsers(context.Background(), db)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "user    %d created, %d total\n", n, len(seeddata.Users))
	return closePool(db, dbURL)
}

func seedProducts(dbURL string) error {
	db, err := store.Open(store.Config{Driver: "sqlite", URL: dbURL}, product.Models()...)
	if err != nil {
		return err
	}
	// The FTS5 index is built here, at image build time, so productsd never has
	// to index anything at boot.
	n, err := seeddata.LoadProducts(context.Background(), db)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "product %d items + FTS5 search index\n", n)
	return closePool(db, dbURL)
}

// closePool checkpoints WAL content before the file gets copied into the
// runtime image. Skipping this can bake in a database with uncommitted WAL.
func closePool(db *gorm.DB, dbURL string) error {
	pool, err := db.DB()
	if err != nil {
		return err
	}
	if err := pool.Close(); err != nil {
		return fmt.Errorf("close pool for %s: %w", dbURL, err)
	}
	return nil
}
