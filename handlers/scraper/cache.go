package handlers

import (
	"os"
	"path/filepath"

	"github.com/cespare/xxhash/v2"
	"github.com/elastic/go-freelru"
	bolt "go.etcd.io/bbolt"
)

var DB *bolt.DB
var LRU *freelru.SyncedLRU[string, bool]

var ephemeralDBPath string
var ephemeralCacheBackend string

// HasCachedData reports whether a post already has cached metadata. It is used
// by the lightweight request protection layer to avoid treating cheap cached
// requests the same as expensive cache misses.
func HasCachedData(postID string) bool {
	if DB == nil || postID == "" {
		return false
	}
	found := false
	_ = DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("data"))
		if b == nil {
			return nil
		}
		found = b.Get([]byte(postID)) != nil
		return nil
	})
	return found
}

// EphemeralCacheBackend identifies where the stateless compatibility cache is
// stored. It is diagnostic only; callers must not depend on a stable path.
func EphemeralCacheBackend() string {
	return ephemeralCacheBackend
}

func hashStringXXHASH(s string) uint32 {
	return uint32(xxhash.Sum64String(s))
}

func initDBAt(path string) error {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return err
	}

	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte("data")); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte("ttl")); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte("fresh")); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists([]byte("negative"))
		return err
	})
	if err != nil {
		db.Close()
		return err
	}

	DB = db
	return nil
}

func InitDB() error {
	ephemeralDBPath = ""
	ephemeralCacheBackend = "persistent"
	return initDBAt("cache.db")
}

// InitEphemeralDB preserves the mature cache semantics without introducing any
// durable state. Prefer /dev/shm when the container runtime exposes it so bbolt
// pages live on tmpfs; otherwise fall back to the container's disposable /tmp.
// Either backend disappears with the replica and requires no volume/restore.
func InitEphemeralDB() error {
	baseDir := ""
	backend := "tmp"
	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		baseDir = "/dev/shm"
		backend = "tmpfs"
	}

	f, err := os.CreateTemp(baseDir, "instafix-cache-*.db")
	if err != nil && baseDir != "" {
		// Some managed runtimes expose /dev/shm but do not permit writes there.
		baseDir = ""
		backend = "tmp"
		f, err = os.CreateTemp(baseDir, "instafix-cache-*.db")
	}
	if err != nil {
		return err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := initDBAt(path); err != nil {
		_ = os.Remove(path)
		return err
	}
	ephemeralDBPath = path
	ephemeralCacheBackend = backend + ":" + filepath.Dir(path)
	return nil
}

func CloseDB() error {
	var closeErr error
	if DB != nil {
		closeErr = DB.Close()
		DB = nil
	}
	if ephemeralDBPath != "" {
		_ = os.Remove(ephemeralDBPath)
		ephemeralDBPath = ""
	}
	ephemeralCacheBackend = ""
	return closeErr
}

func InitLRU(maxEntries int) {
	// Initialize LRU for grid caching
	lru, err := freelru.NewSynced[string, bool](uint32(maxEntries), hashStringXXHASH)
	if err != nil {
		panic(err)
	}

	lru.SetOnEvict(func(key string, value bool) {
		os.Remove(key)
	})

	// Fill LRU with existing files
	dir, err := os.ReadDir("static")
	if err != nil {
		panic(err)
	}
	for _, d := range dir {
		if !d.IsDir() {
			lru.Add("static/"+d.Name(), true)
		}
	}

	LRU = lru
}
