package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
)

var (
	mu        sync.RWMutex
	cache     = map[string]string{}
	debugMode bool
)

func SetDebugMode(v bool) {
	mu.Lock()
	defer mu.Unlock()
	debugMode = v
	if v {
		cache = map[string]string{}
	}
}

func VersionedPath(assetPath string) string {
	mu.RLock()
	if !debugMode {
		if h, ok := cache[assetPath]; ok {
			mu.RUnlock()
			return assetPath + "?v=" + h
		}
	}
	mu.RUnlock()

	data, err := os.ReadFile(assetPath)
	if err != nil {
		return assetPath
	}
	hash := sha256.Sum256(data)
	h := hex.EncodeToString(hash[:])[:8]

	if !debugMode {
		mu.Lock()
		cache[assetPath] = h
		mu.Unlock()
	}
	return assetPath + "?v=" + h
}
