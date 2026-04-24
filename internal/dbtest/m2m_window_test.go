package dbtest_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/feature"
)

func TestM2MWindowFunction(t *testing.T) {

	_testEachDB := func(t *testing.T, f func(t *testing.T, dbName string, db *bun.DB)) {
		for dbName, newDB := range map[string]func(tb testing.TB) *bun.DB{
			sqliteName: sqlite,
		} {
			t.Run(dbName, func(t *testing.T) {
				f(t, dbName, newDB(t))
			})
		}
	}

	_testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
		if !db.Dialect().Features().Has(feature.WindowFunctions) {
			t.Skip("dialect does not support window functions")
		}

		createTestSchema(t, db)
		loadTestData(t, ctx, db)

		t.Run("M2M with limit", func(t *testing.T) {
			testM2MWithLimit(t, db)
		})
		t.Run("M2M with limit and offset", func(t *testing.T) {
			testM2MWithLimitAndOffset(t, db)
		})
		t.Run("M2M with order by", func(t *testing.T) {
			testM2MWithOrderBy(t, db)
		})
		t.Run("M2M with order by and limit", func(t *testing.T) {
			testM2MWithOrderByAndLimit(t, db)
		})
		t.Run("M2M with limit larger than total", func(t *testing.T) {
			testM2MWithLimitLargerThanTotal(t, db)
		})
		t.Run("M2M with offset larger than total", func(t *testing.T) {
			testM2MWithOffsetLargerThanTotal(t, db)
		})
		t.Run("M2M with limit zero", func(t *testing.T) {
			testM2MWithLimitZero(t, db)
		})
		t.Run("M2M with offset zero", func(t *testing.T) {
			testM2MWithOffsetZero(t, db)
		})
		t.Run("M2M with complex order by", func(t *testing.T) {
			testM2MWithComplexOrderBy(t, db)
		})
	})
}

// testM2MWithLimit tests basic limit functionality with window function
func testM2MWithLimit(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2), Book 101 has 1 genre (1)
	// With limit 1, each book should get at most 1 genre
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Limit(1).OrderExpr("genre.id ASC")
		}).
		OrderExpr("book.id ASC").
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 3)

	// Book 100 should have 1 genre (genre 1)
	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 1)
	require.Equal(t, 1, books[0].Genres[0].ID)

	// Book 101 should have 1 genre (genre 1)
	require.Equal(t, 101, books[1].ID)
	require.Len(t, books[1].Genres, 1)
	require.Equal(t, 1, books[1].Genres[0].ID)

	// Book 102 has no genres
	require.Equal(t, 102, books[2].ID)
	require.Len(t, books[2].Genres, 0)
}

// testM2MWithLimitAndOffset tests limit and offset with window function
func testM2MWithLimitAndOffset(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With limit 1, offset 1, should get only genre 2
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Limit(1).Offset(1).OrderExpr("genre.id ASC")
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	// Book 100 should have 1 genre (genre 2) due to offset 1
	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 1)
	require.Equal(t, 2, books[0].Genres[0].ID)
}

// testM2MWithOrderBy tests order by with window function
func testM2MWithOrderBy(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With order by genre.id DESC and limit 1, should get genre 2
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.OrderExpr("genre.id DESC").Limit(1)
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	// Book 100 should have 1 genre (genre 2) due to DESC order
	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 1)
	require.Equal(t, 2, books[0].Genres[0].ID)
}

// testM2MWithOrderByAndLimit tests combined order by and limit
func testM2MWithOrderByAndLimit(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With order by genre.id DESC and limit 1, should get genre 2
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.OrderExpr("genre.id DESC").Limit(1)
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 1)
	require.Equal(t, 2, books[0].Genres[0].ID)
}

// testM2MWithLimitLargerThanTotal tests limit larger than total records
func testM2MWithLimitLargerThanTotal(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With limit 10, should get all 2 genres
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Limit(10).OrderExpr("genre.id ASC")
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 2)
	require.Equal(t, 1, books[0].Genres[0].ID)
	require.Equal(t, 2, books[0].Genres[1].ID)
}

// testM2MWithOffsetLargerThanTotal tests offset larger than total records
func testM2MWithOffsetLargerThanTotal(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With offset 10, should get 0 genres
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Offset(10).OrderExpr("genre.id ASC")
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 0)
}

// testM2MWithLimitZero tests limit zero (should return all records)
func testM2MWithLimitZero(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With limit 0, should get all 2 genres
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Limit(0).OrderExpr("genre.id ASC")
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 2)
	require.Equal(t, 1, books[0].Genres[0].ID)
	require.Equal(t, 2, books[0].Genres[1].ID)
}

// testM2MWithOffsetZero tests offset zero (should start from beginning)
func testM2MWithOffsetZero(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With offset 0 and limit 1, should get genre 1
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Offset(0).Limit(1).OrderExpr("genre.id ASC")
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 1)
	require.Equal(t, 1, books[0].Genres[0].ID)
}

// testM2MWithComplexOrderBy tests complex order by with multiple columns
func testM2MWithComplexOrderBy(t *testing.T, db *bun.DB) {
	// Book 100 has 2 genres (1, 2)
	// With order by genre.name DESC and limit 1
	var books []Book
	err := db.NewSelect().
		Model(&books).
		Column("book.*").
		Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.OrderExpr("genre.name DESC").Limit(1)
		}).
		Where("book.id = ?", 100).
		Scan(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)

	require.Equal(t, 100, books[0].ID)
	require.Len(t, books[0].Genres, 1)
	// genre 2 ("genre 2") should come before genre 1 ("genre 1") in DESC order
	require.Equal(t, 2, books[0].Genres[0].ID)
}

// TestM2MWindowFunctionBidirectional tests m2m window function in both directions
func TestM2MWindowFunctionBidirectional(t *testing.T) {
	testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
		if !db.Dialect().Features().Has(feature.WindowFunctions) {
			t.Skip("dialect does not support window functions")
		}

		createTestSchema(t, db)
		loadTestData(t, ctx, db)

		// Test from Genre -> Books direction
		var genres []Genre
		err := db.NewSelect().
			Model(&genres).
			Column("genre.*").
			Relation("Books", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Limit(1).OrderExpr("book.id ASC")
			}).
			OrderExpr("genre.id ASC").
			Scan(ctx)
		require.NoError(t, err)
		require.Len(t, genres, 4)

		// Genre 1 has 2 books (100, 101), with limit 1 should get book 100
		require.Equal(t, 1, genres[0].ID)
		require.Len(t, genres[0].Books, 1)
		require.Equal(t, 100, genres[0].Books[0].ID)

		// Genre 2 has 1 book (100), with limit 1 should get book 100
		require.Equal(t, 2, genres[1].ID)
		require.Len(t, genres[1].Books, 1)
		require.Equal(t, 100, genres[1].Books[0].ID)
	})
}

// TestM2MWindowFunctionWithAdditionalConditions tests m2m window function with additional WHERE conditions
func TestM2MWindowFunctionWithAdditionalConditions(t *testing.T) {
	testEachDB(t, func(t *testing.T, dbName string, db *bun.DB) {
		if !db.Dialect().Features().Has(feature.WindowFunctions) {
			t.Skip("dialect does not support window functions")
		}

		createTestSchema(t, db)
		loadTestData(t, ctx, db)

		// Test with additional WHERE condition on the relation
		var books []Book
		err := db.NewSelect().
			Model(&books).
			Column("book.*").
			Relation("Genres", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Limit(1).Where("genre.id > ?", 1).OrderExpr("genre.id ASC")
			}).
			Where("book.id = ?", 100).
			Scan(ctx)
		require.NoError(t, err)
		require.Len(t, books, 1)

		require.Equal(t, 100, books[0].ID)
		require.Len(t, books[0].Genres, 1)
		// With genre.id > 1 and limit 1, should get genre 2
		require.Equal(t, 2, books[0].Genres[0].ID)
	})
}
