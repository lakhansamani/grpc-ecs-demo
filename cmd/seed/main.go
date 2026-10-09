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
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"gorm.io/gorm"

	"github.com/lakhansamani/grpc-ecs-ecom/internal/platform/store"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/product"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/user"
)

// SeedUsers are fixed so the demo is reproducible and the passwords can go on
// a slide. Demo credentials for a throwaway environment, nothing more.
var SeedUsers = []struct{ Name, Email, Password string }{
	{"Demo User", "demo@example.com", "demo-password"},
	{"Asha Patel", "asha@example.com", "demo-password"},
	{"Ravi Mehta", "ravi@example.com", "demo-password"},
}

// SeedProducts is a small catalogue with deliberate edge cases: one item out
// of stock and one with only a single unit left, so a rejected order can be
// demonstrated without editing data on stage.
var SeedProducts = []product.Product{
	{ID: "p-1001", SKU: "SONY-WH1000XM5", Title: "Wireless Noise Cancelling Headphones", Brand: "Sony", Category: "audio", PriceMinor: 2999900, Currency: "INR", Stock: 42},
	{ID: "p-1002", SKU: "BOSE-QC-ULTRA", Title: "Noise Cancelling Earbuds Ultra", Brand: "Bose", Category: "audio", PriceMinor: 2299900, Currency: "INR", Stock: 11},
	{ID: "p-1003", SKU: "JBL-FLIP-6", Title: "Portable Bluetooth Speaker", Brand: "JBL", Category: "audio", PriceMinor: 999900, Currency: "INR", Stock: 0},
	{ID: "p-1004", SKU: "NIKE-PEGASUS-41", Title: "Running Shoes Lightweight Mesh", Brand: "Nike", Category: "footwear", PriceMinor: 1199500, Currency: "INR", Stock: 7},
	{ID: "p-1005", SKU: "ADIDAS-ULTRA-5", Title: "Ultraboost Running Shoes", Brand: "Adidas", Category: "footwear", PriceMinor: 1699900, Currency: "INR", Stock: 1},
	{ID: "p-1006", SKU: "APPLE-IPAD-A16", Title: "Tablet 11 inch Liquid Retina", Brand: "Apple", Category: "computing", PriceMinor: 5990000, Currency: "INR", Stock: 15},
	{ID: "p-1007", SKU: "DELL-XPS-13", Title: "Ultrabook Laptop 13 inch", Brand: "Dell", Category: "computing", PriceMinor: 12499000, Currency: "INR", Stock: 4},
	{ID: "p-1008", SKU: "LOGI-MX-MASTER4", Title: "Wireless Ergonomic Mouse", Brand: "Logitech", Category: "computing", PriceMinor: 999000, Currency: "INR", Stock: 63},
	{ID: "p-1009", SKU: "KINDLE-PW-12", Title: "E Reader Paperwhite Waterproof", Brand: "Amazon", Category: "reading", PriceMinor: 1599900, Currency: "INR", Stock: 23},
	{ID: "p-1010", SKU: "MI-BAND-9", Title: "Fitness Band Heart Rate Monitor", Brand: "Xiaomi", Category: "wearable", PriceMinor: 349900, Currency: "INR", Stock: 120},
	{ID: "p-1011", SKU: "SAMS-WATCH-7", Title: "Smartwatch AMOLED GPS", Brand: "Samsung", Category: "wearable", PriceMinor: 2799900, Currency: "INR", Stock: 9},
	{ID: "p-1012", SKU: "ANKER-737-PB", Title: "Power Bank 24000mAh Fast Charge", Brand: "Anker", Category: "accessories", PriceMinor: 899900, Currency: "INR", Stock: 31},
}

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
	s := user.NewStore(db)
	ctx := context.Background()
	for _, u := range SeedUsers {
		created, err := s.Create(ctx, u.Name, u.Email, u.Password)
		switch {
		case err == nil:
			fmt.Fprintf(os.Stdout, "user    %s (%s)\n", u.Email, created.ID)
		case errors.Is(err, user.ErrEmailTaken):
			fmt.Fprintf(os.Stdout, "user    %s (exists)\n", u.Email)
		default:
			return fmt.Errorf("create %s: %w", u.Email, err)
		}
	}
	return closePool(db, dbURL)
}

func seedProducts(dbURL string) error {
	db, err := store.Open(store.Config{Driver: "sqlite", URL: dbURL}, product.Models()...)
	if err != nil {
		return err
	}
	for _, p := range SeedProducts {
		// Idempotent: re-running the seeder must not fail an image build.
		if err := db.Save(&p).Error; err != nil {
			return fmt.Errorf("save %s: %w", p.SKU, err)
		}
	}
	// The FTS5 index is built here, at image build time, so productsd never
	// has to index anything at boot.
	if err := product.BuildSearchIndex(db); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "product %d items + FTS5 search index\n", len(SeedProducts))
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
