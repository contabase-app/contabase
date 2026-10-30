package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"time"
)

const hashLength = 8

type cachedVersion struct {
	hash    string
	modTime time.Time
	size    int64
}

var (
	mu    sync.RWMutex
	cache = map[string]cachedVersion{}
)

// SetDebugMode clears the cache when the server runs in debug mode. The cache
// is always validated against the file metadata, so a CSS rebuild is picked up
// without restarting the process.
func SetDebugMode(_ bool) {
	mu.Lock()
	defer mu.Unlock()
	cache = map[string]cachedVersion{}
}

// VersionedPath appends a stable content hash to a filesystem asset path.
// It returns the original path when the file cannot be read.
func VersionedPath(assetPath string) string {
	hash, ok := ContentHash(assetPath)
	if !ok {
		return assetPath
	}
	return assetPath + "?v=" + hash
}

// ContentHash returns the content hash used for cache busting. The result stays
// stable while the file content is unchanged and changes when it is rewritten.
// The cache avoids re-reading unchanged files; a stat call is enough to detect
// changes between requests.
func ContentHash(assetPath string) (string, bool) {
	before, err := os.Stat(assetPath)
	if err != nil || before.IsDir() {
		return "", false
	}

	mu.RLock()
	entry, ok := cache[assetPath]
	mu.RUnlock()
	if ok && entry.size == before.Size() && entry.modTime.Equal(before.ModTime()) {
		return entry.hash, true
	}

	data, err := os.ReadFile(assetPath)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])[:hashLength]

	// Do not cache a read that raced with a write; the next call will retry.
	if after, err := os.Stat(assetPath); err == nil &&
		after.Size() == before.Size() && after.ModTime().Equal(before.ModTime()) {
		mu.Lock()
		cache[assetPath] = cachedVersion{
			hash:    hash,
			modTime: before.ModTime(),
			size:    before.Size(),
		}
		mu.Unlock()
	}

	return hash, true
}
