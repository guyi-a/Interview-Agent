package webfetch

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// URLCache 是一个带 TTL + 总字节上限的 LRU 缓存，key 是 (url, maxBytes)。
//
// 参考 krow-agent 的设计：
//   - 命中：数据 age < TTL 才返回
//   - 写入：如果超总字节 / 总条目，从队头（LRU）淘汰
//   - 只缓存成功抓取（2xx）；tool wrapper 判断后再调 Set
type URLCache struct {
	mu           sync.Mutex
	items        map[string]*list.Element // key → element in lru
	lru          *list.List               // 队头是最老，队尾是最新
	maxEntries   int
	maxSizeBytes int
	ttl          time.Duration
	totalSize    int
}

type cacheEntry struct {
	key       string
	result    *Result
	timestamp time.Time
	sizeBytes int
}

const (
	CacheDefaultMaxEntries = 100
	CacheDefaultMaxBytes   = 50 * 1024 * 1024 // 50 MB
	CacheDefaultTTL        = 15 * time.Minute
)

func NewCache(maxEntries, maxSizeBytes int, ttl time.Duration) *URLCache {
	if maxEntries <= 0 {
		maxEntries = CacheDefaultMaxEntries
	}
	if maxSizeBytes <= 0 {
		maxSizeBytes = CacheDefaultMaxBytes
	}
	if ttl <= 0 {
		ttl = CacheDefaultTTL
	}
	return &URLCache{
		items:        make(map[string]*list.Element),
		lru:          list.New(),
		maxEntries:   maxEntries,
		maxSizeBytes: maxSizeBytes,
		ttl:          ttl,
	}
}

func makeCacheKey(url string, maxBytes int) string {
	h := sha256.Sum256([]byte(url + "|" + itoa(maxBytes)))
	return hex.EncodeToString(h[:16])
}

func itoa(n int) string {
	// 避免 strconv import 循环风格 —— 简单实现（缓存 key 用，性能不敏感）
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Get 返命中的结果；miss 或 expired 返 nil。
func (c *URLCache) Get(url string, maxBytes int) *Result {
	key := makeCacheKey(url, maxBytes)
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil
	}
	entry := el.Value.(*cacheEntry)
	if time.Since(entry.timestamp) > c.ttl {
		c.removeElement(el)
		return nil
	}
	c.lru.MoveToBack(el) // 最近使用
	return entry.result
}

// Set 写入缓存。size 按 result.Text 长度 + 256 (header 冗余) 估算。
func (c *URLCache) Set(url string, maxBytes int, result *Result) {
	if result == nil {
		return
	}
	key := makeCacheKey(url, maxBytes)
	size := len(result.Text) + 256

	c.mu.Lock()
	defer c.mu.Unlock()

	// 已在缓存里 → 先扔了
	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}

	// LRU 淘汰：数量或大小超限时从队头（最老）踢
	for c.lru.Len() > 0 && (c.lru.Len() >= c.maxEntries || c.totalSize+size > c.maxSizeBytes) {
		c.removeElement(c.lru.Front())
	}

	entry := &cacheEntry{
		key:       key,
		result:    result,
		timestamp: time.Now(),
		sizeBytes: size,
	}
	el := c.lru.PushBack(entry)
	c.items[key] = el
	c.totalSize += size
}

// Clear 清空缓存（tests / debug 用）。
func (c *URLCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.lru.Init()
	c.totalSize = 0
}

func (c *URLCache) removeElement(el *list.Element) {
	entry := el.Value.(*cacheEntry)
	delete(c.items, entry.key)
	c.lru.Remove(el)
	c.totalSize -= entry.sizeBytes
}
