package dbtest_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/feature"
)

func TestHasManyWindowFunction(t *testing.T) {
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

		// Define models with has-many relation.
		type Profile struct {
			ID     int64 `bun:",pk"`
			Name   string
			Lang   string
			UserID int64
		}

		type User struct {
			bun.BaseModel `bun:"alias:u"`
			ID            int64 `bun:",pk"`
			Name          string
			Profiles      []*Profile `bun:"rel:has-many,join:id=user_id"`
		}

		mustResetModel(t, ctx, db, (*User)(nil), (*Profile)(nil))

		// Insert test data:
		// User 1: 3 profiles (1, 2, 3)
		// User 2: 2 profiles (4, 5)
		// User 3: 0 profiles
		users := []*User{
			{ID: 1, Name: "user 1"},
			{ID: 2, Name: "user 2"},
			{ID: 3, Name: "user 3"},
		}
		_, err := db.NewInsert().Model(&users).Exec(ctx)
		require.NoError(t, err)

		profiles := []*Profile{
			{ID: 1, Name: "name1", Lang: "en", UserID: 1},
			{ID: 2, Name: "name2", Lang: "ru", UserID: 1},
			{ID: 3, Name: "name3", Lang: "ja", UserID: 1},
			{ID: 4, Name: "name4", Lang: "en", UserID: 2},
			{ID: 5, Name: "name5", Lang: "ru", UserID: 2},
		}
		_, err = db.NewInsert().Model(&profiles).Exec(ctx)
		require.NoError(t, err)

		t.Run("limit", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(1).OrderExpr("profile.id ASC")
				}).
				OrderExpr("u.id ASC").
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 3)

			// User 1 has 3 profiles, limit 1 -> 1 profile (id=1)
			require.Equal(t, int64(1), outUsers[0].ID)
			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, int64(1), outUsers[0].Profiles[0].ID)

			// User 2 has 2 profiles, limit 1 -> 1 profile (id=4)
			require.Equal(t, int64(2), outUsers[1].ID)
			require.Len(t, outUsers[1].Profiles, 1)
			require.Equal(t, int64(4), outUsers[1].Profiles[0].ID)

			// User 3 has 0 profiles
			require.Equal(t, int64(3), outUsers[2].ID)
			require.Len(t, outUsers[2].Profiles, 0)
		})

		t.Run("limit_and_offset", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(1).Offset(1).OrderExpr("profile.id ASC")
				}).
				OrderExpr("u.id ASC").
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 3)

			// User 1 has 3 profiles, limit 1 offset 1 -> profile id=2
			require.Equal(t, int64(1), outUsers[0].ID)
			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, int64(2), outUsers[0].Profiles[0].ID)

			// User 2 has 2 profiles, limit 1 offset 1 -> profile id=5
			require.Equal(t, int64(2), outUsers[1].ID)
			require.Len(t, outUsers[1].Profiles, 1)
			require.Equal(t, int64(5), outUsers[1].Profiles[0].ID)
		})

		t.Run("limit_and_offset_2", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(1).Offset(2).OrderExpr("profile.id ASC")
				}).
				OrderExpr("u.id ASC").
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 3)

			// User 1 has 3 profiles, limit 1 offset 2 -> profile id=3
			require.Equal(t, int64(1), outUsers[0].ID)
			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, int64(3), outUsers[0].Profiles[0].ID)

			// User 2 has 2 profiles, limit 1 offset 2 -> 0 profiles
			require.Equal(t, int64(2), outUsers[1].ID)
			require.Len(t, outUsers[1].Profiles, 0)
		})

		t.Run("limit_larger_than_total", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(10).OrderExpr("profile.id ASC")
				}).
				OrderExpr("u.id ASC").
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 3)

			// User 1 has 3 profiles, limit 10 -> 3 profiles
			require.Equal(t, int64(1), outUsers[0].ID)
			require.Len(t, outUsers[0].Profiles, 3)

			// User 2 has 2 profiles, limit 10 -> 2 profiles
			require.Equal(t, int64(2), outUsers[1].ID)
			require.Len(t, outUsers[1].Profiles, 2)
		})

		t.Run("offset_larger_than_total", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Offset(10).OrderExpr("profile.id ASC")
				}).
				OrderExpr("u.id ASC").
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 3)

			// All users should have 0 profiles
			for _, u := range outUsers {
				require.Len(t, u.Profiles, 0)
			}
		})

		t.Run("order_by_desc", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(1).OrderExpr("profile.id DESC")
				}).
				Where("u.id = ?", 1).
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 1)

			// User 1 has 3 profiles (1,2,3), limit 1 DESC -> profile id=3
			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, int64(3), outUsers[0].Profiles[0].ID)
		})

		t.Run("order_by_name", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(1).OrderExpr("profile.name DESC")
				}).
				Where("u.id = ?", 1).
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 1)

			// User 1 has profiles name1, name2, name3
			// limit 1 DESC by name -> name3
			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, "name3", outUsers[0].Profiles[0].Name)
		})

		t.Run("offset_zero", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Offset(0).Limit(1).OrderExpr("profile.id ASC")
				}).
				Where("u.id = ?", 1).
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 1)

			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, int64(1), outUsers[0].Profiles[0].ID)
		})

		t.Run("limit_zero_returns_all", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(0).OrderExpr("profile.id ASC")
				}).
				Where("u.id = ?", 1).
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 1)

			// limit 0 means no limit -> all 3 profiles
			require.Len(t, outUsers[0].Profiles, 3)
		})

		t.Run("with_additional_where", func(t *testing.T) {
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(1).Where("profile.lang = ?", "ru").OrderExpr("profile.id ASC")
				}).
				Where("u.id = ?", 1).
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 1)

			// User 1 has profiles with lang=en,ru,ja; filter lang=ru + limit 1 -> name2
			require.Len(t, outUsers[0].Profiles, 1)
			require.Equal(t, int64(2), outUsers[0].Profiles[0].ID)
			require.Equal(t, "name2", outUsers[0].Profiles[0].Name)
		})

		t.Run("multiple_users_independent_limit", func(t *testing.T) {
			// Verify each user gets exactly limit records independently
			var outUsers []*User
			err := db.NewSelect().
				Model(&outUsers).
				Column("u.*").
				Relation("Profiles", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Limit(2).OrderExpr("profile.id ASC")
				}).
				OrderExpr("u.id ASC").
				Scan(ctx)
			require.NoError(t, err)
			require.Len(t, outUsers, 3)

			// User 1 has 3 profiles, limit 2 -> 2 profiles (id=1,2)
			require.Equal(t, int64(1), outUsers[0].ID)
			require.Len(t, outUsers[0].Profiles, 2)
			require.Equal(t, int64(1), outUsers[0].Profiles[0].ID)
			require.Equal(t, int64(2), outUsers[0].Profiles[1].ID)

			// User 2 has 2 profiles, limit 2 -> 2 profiles (id=4,5)
			require.Equal(t, int64(2), outUsers[1].ID)
			require.Len(t, outUsers[1].Profiles, 2)
			require.Equal(t, int64(4), outUsers[1].Profiles[0].ID)
			require.Equal(t, int64(5), outUsers[1].Profiles[1].ID)

			// User 3 has 0 profiles, limit 2 -> 0 profiles
			require.Equal(t, int64(3), outUsers[2].ID)
			require.Len(t, outUsers[2].Profiles, 0)
		})
	})
}
