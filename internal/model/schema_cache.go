package model

import (
	"fmt"
	"reflect"
	"sync"

	"gorm.io/gorm"
)

type schemaTableKey struct {
	pool any
	name string
}

var schemaTableCache sync.Map

// HasTableCached checks whether the target table/model exists, caching positive
// results per underlying *sql.DB connection pool to avoid repeated
// information_schema queries on request hot paths while remaining safe for
// partial-schema unit tests.
func HasTableCached(db *gorm.DB, dst any) bool {
	if db == nil {
		return false
	}
	keyName := ""
	switch v := dst.(type) {
	case string:
		keyName = v
	default:
		t := reflect.TypeOf(dst)
		for t != nil && t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t != nil {
			keyName = t.PkgPath() + "." + t.Name()
		} else {
			keyName = fmt.Sprintf("%T", dst)
		}
	}

	var poolKey any
	if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
		poolKey = sqlDB
	}
	if poolKey != nil && keyName != "" {
		cacheKey := schemaTableKey{pool: poolKey, name: keyName}
		if _, ok := schemaTableCache.Load(cacheKey); ok {
			return true
		}
		if db.Migrator().HasTable(dst) {
			schemaTableCache.Store(cacheKey, struct{}{})
			return true
		}
		return false
	}
	return db.Migrator().HasTable(dst)
}
