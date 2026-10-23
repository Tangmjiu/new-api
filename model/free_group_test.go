package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFreeGroupPersistenceMatrix(t *testing.T) {
	cases := []struct {
		name    string
		dialect gorm.Dialector
	}{
		{"sqlite", sqlite.Open(filepath.Join(t.TempDir(), "free.db"))},
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		cases = append(cases, struct {
			name    string
			dialect gorm.Dialector
		}{"mysql", mysql.Open(dsn)})
	}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		cases = append(cases, struct {
			name    string
			dialect gorm.Dialector
		}{"postgres", postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(tc.dialect, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			oldDB := DB
			DB = db
			t.Cleanup(func() { DB = oldDB })
			schemas := []any{&FreeGroupConfigRecord{}, &FreeGroupConfigVersion{}, &FreeGroupMember{}, &FreeGroupAudit{}}
			for _, schema := range schemas {
				require.NoError(t, db.Migrator().DropTable(schema))
			}
			t.Cleanup(func() {
				for _, schema := range schemas {
					_ = db.Migrator().DropTable(schema)
				}
			})
			// New tables coexist with existing production user/token schemas. Repeated
			// migration must neither replace data nor append seed/history records.
			for range 2 {
				require.NoError(t, db.AutoMigrate(schemas...))
				require.NoError(t, InitializeFreeGroupConfig())
			}
			current, err := GetFreeGroupConfigRecord()
			require.NoError(t, err)
			assert.EqualValues(t, 1, current.Version)
			var config FreeGroupConfig
			require.NoError(t, common.UnmarshalJsonStr(current.Config, &config))
			assert.False(t, config.Enabled)
			assert.False(t, config.Queue.Enabled)
			assert.True(t, config.Models.WhitelistEnabled)
			assert.Empty(t, config.Models.Models)
			config.Enabled = true
			encoded, err := common.Marshal(config)
			require.NoError(t, err)
			next := &FreeGroupConfigRecord{Version: 1, Preset: "custom", Config: string(encoded), UpdatedBy: 17}
			version := &FreeGroupConfigVersion{Config: string(encoded), UpdatedBy: 17, HighRisk: true, Summary: "Explicit administrator change"}
			require.NoError(t, SaveFreeGroupConfig(next, version))
			assert.EqualValues(t, 2, version.Version)
			assert.ErrorIs(t, SaveFreeGroupConfig(next, &FreeGroupConfigVersion{Config: "{}"}), ErrFreeGroupConfigVersionConflict)
			for range 2 {
				require.NoError(t, db.AutoMigrate(schemas...))
				require.NoError(t, InitializeFreeGroupConfig())
			}
			current, err = GetFreeGroupConfigRecord()
			require.NoError(t, err)
			assert.EqualValues(t, 2, current.Version)
			assert.Equal(t, string(encoded), current.Config)
			versions, total, err := ListFreeGroupConfigVersions(0, 50)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			assert.Len(t, versions, 2)
			require.NoError(t, UpsertFreeGroupMember(&FreeGroupMember{UserID: 1, Status: FreeGroupMemberBlocked, Source: "manual", UpdatedBy: 17}))
			require.NoError(t, UpsertFreeGroupMember(&FreeGroupMember{UserID: 1, Status: FreeGroupMemberPending, Source: "application"}))
			var member FreeGroupMember
			require.NoError(t, db.Where("user_id = ?", 1).First(&member).Error)
			assert.Equal(t, FreeGroupMemberBlocked, member.Status)
			assert.Error(t, db.Create(&FreeGroupMember{UserID: 1, Status: FreeGroupMemberApproved}).Error)
			var auditCount int64
			require.NoError(t, db.Model(&FreeGroupAudit{}).Count(&auditCount).Error)
			assert.EqualValues(t, 1, auditCount)
			require.NoError(t, RemoveFreeGroupMember(1, 17, current.Version))
			require.NoError(t, RemoveFreeGroupMember(1, 17, current.Version))
			var memberCount int64
			require.NoError(t, db.Model(&FreeGroupMember{}).Where("user_id = ?", 1).Count(&memberCount).Error)
			assert.Zero(t, memberCount)
			require.NoError(t, db.Model(&FreeGroupAudit{}).Count(&auditCount).Error)
			assert.EqualValues(t, 2, auditCount)
		})
	}
}

func TestFreeGroupReservedUserMembership(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	assert.Error(t, db.Create(&User{Username: "invalid", Group: FreeGroupName}).Error)
	user := &User{Username: "paid", Group: "default"}
	require.NoError(t, db.Create(user).Error)
	assert.Error(t, db.Model(&User{}).Where("id = ?", user.Id).Update("group", FreeGroupName).Error)
	var saved User
	require.NoError(t, db.First(&saved, user.Id).Error)
	assert.Equal(t, "default", saved.Group)
	_, err = MigrateTokenGroup(" free ", "default")
	assert.ErrorContains(t, err, "cannot be migrated by traditional group operations")
	_, err = MigrateTokenGroup("default", " free ")
	assert.ErrorContains(t, err, "cannot be migrated by traditional group operations")
}
