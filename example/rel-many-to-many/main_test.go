package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"
	"github.com/uptrace/bun/extra/bundebug"
)

func TestM2MWindowFunction(t *testing.T) {
	ctx := context.Background()

	sqldb, err := sql.Open(sqliteshim.ShimName, "file::memory:?cache=shared")
	if err != nil {
		panic(err)
	}

	db := bun.NewDB(sqldb, sqlitedialect.New()).
		WithQueryHook(bundebug.NewQueryHook(bundebug.WithVerbose(true)))
	defer db.Close()

	// Register many to many model so bun can better recognize m2m relation.
	db.RegisterModel((*OrderToItem)(nil))

	if err := createSchema(ctx, db); err != nil {
		panic(err)
	}

	// Test 1: Basic m2m query with limit
	t.Run("M2M with limit", func(t *testing.T) {
		order := new(Order)
		if err := db.NewSelect().
			Model(order).
			Relation("Items", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Limit(1)
			}).
			Order("order.id ASC").
			Scan(ctx); err != nil {
			t.Fatalf("failed to scan order: %v", err)
		}
		fmt.Println("Order", order.ID, "Items", len(order.Items))
		if len(order.Items) != 1 {
			t.Errorf("expected 1 item, got %d", len(order.Items))
		}
	})

	// Test 2: M2M query with limit and offset
	t.Run("M2M with limit and offset", func(t *testing.T) {
		item := new(Item)
		if err := db.NewSelect().
			Model(item).
			Relation("Orders", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Limit(1).Offset(1)
			}).
			Order("item.id ASC").
			Scan(ctx); err != nil {
			t.Fatalf("failed to scan item: %v", err)
		}
		fmt.Println("Item", item.ID, "Orders", len(item.Orders))
		// Item 1 has 1 order (Order 1), Item 2 has 1 order (Order 1)
		// With limit 1, offset 1, we should get 0 orders for item 1
		// and 0 orders for item 2 (since there's only 1 order each)
	})

	// Test 3: M2M query with order by
	t.Run("M2M with order by", func(t *testing.T) {
		order := new(Order)
		if err := db.NewSelect().
			Model(order).
			Relation("Items", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.OrderExpr("item.id DESC").Limit(1)
			}).
			Order("order.id ASC").
			Scan(ctx); err != nil {
			t.Fatalf("failed to scan order: %v", err)
		}
		fmt.Println("Order", order.ID, "Items", len(order.Items))
		if len(order.Items) != 1 {
			t.Errorf("expected 1 item, got %d", len(order.Items))
		}
	})
}