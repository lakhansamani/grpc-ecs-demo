// Package seeddata holds the demo catalogue and demo users, plus idempotent
// functions to load them.
//
// It exists because the data is now needed in two places:
//
//	cmd/seed            bakes it into a SQLite file at IMAGE BUILD time
//	userd / productsd   load it at BOOT when pointed at a shared database
//
// With SQLite the database ships inside the image, so seeding at build time is
// right. With a shared Postgres there is no file to bake, so the services seed
// at boot instead — and because several tasks boot at once, every function here
// must be safe to run concurrently and repeatedly. They are: users go in via
// Create and tolerate ErrEmailTaken, products use Save (upsert by primary key),
// and the search index is IF NOT EXISTS on Postgres.
//
// It imports product and user rather than the other way round, so those
// packages stay free of demo fixtures.
package seeddata

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/lakhansamani/grpc-ecs-demo/internal/product"
	"github.com/lakhansamani/grpc-ecs-demo/internal/user"
)

// Users are fixed so the demo is reproducible and the passwords can go on a
// slide. Demo credentials for a throwaway environment, nothing more.
//
// These are the accounts to authenticate as once userd runs more than one task
// with SQLite: a user created by Register lands on exactly one task.
var Users = []struct{ Name, Email, Password string }{
	{"Demo User", "demo@example.com", "demo-password"},
	{"Asha Patel", "asha@example.com", "demo-password"},
	{"Ravi Mehta", "ravi@example.com", "demo-password"},
}

// Products is a small catalogue with deliberate edge cases: p-1003 is out of
// stock and p-1005 has a single unit left, so a rejected order can be shown
// without editing data on stage.
var Products = []product.Product{
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

// LoadUsers inserts the demo users. Safe to call again, and safe to call from
// several tasks at the same time: the email column is unique, so a loser of the
// race gets ErrEmailTaken and carries on.
func LoadUsers(ctx context.Context, db *gorm.DB) (created int, err error) {
	s := user.NewStore(db)
	for _, u := range Users {
		switch _, err := s.Create(ctx, u.Name, u.Email, u.Password); {
		case err == nil:
			created++
		case errors.Is(err, user.ErrEmailTaken):
			// already there - the point of an idempotent seeder
		default:
			return created, fmt.Errorf("seeddata: create %s: %w", u.Email, err)
		}
	}
	return created, nil
}

// LoadProducts upserts the catalogue and builds the search index.
func LoadProducts(ctx context.Context, db *gorm.DB) (int, error) {
	for i := range Products {
		p := Products[i]
		if err := db.WithContext(ctx).Save(&p).Error; err != nil {
			return 0, fmt.Errorf("seeddata: save %s: %w", p.SKU, err)
		}
	}
	if err := product.BuildSearchIndex(db); err != nil {
		return 0, err
	}
	return len(Products), nil
}
