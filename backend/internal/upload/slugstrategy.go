package upload

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"
)

// Slug strategies control how an auto-generated share slug is named. A custom
// user-supplied slug (?slug=) always takes precedence over any strategy.
const (
	// SlugStrategyRandom is the default: a random slug from RandomSlug (current
	// behavior), e.g. "aB3kdq".
	SlugStrategyRandom = "random"
	// SlugStrategyGfycat produces "adjective-adjective-animal-suffix", e.g.
	// "brave-quick-otter-k3m9".
	SlugStrategyGfycat = "gfycat"
	// SlugStrategyDate prefixes a UTC date to a random slug, e.g.
	// "2026-07-19-aB3kdq".
	SlugStrategyDate = "date"
)

// ValidSlugStrategy reports whether s is one of the supported strategies. The
// match is exact (canonical lowercase); callers normalize input beforehand.
func ValidSlugStrategy(s string) bool {
	switch s {
	case SlugStrategyRandom, SlugStrategyGfycat, SlugStrategyDate:
		return true
	default:
		return false
	}
}

// GenerateSlug builds an auto-generated slug for the given strategy. n is the
// length passed to RandomSlug for the random portion (random/date strategies).
//
// Every returned slug is guaranteed URL-safe: it contains only [A-Za-z0-9-],
// never a '.', and is at least 4 bytes long, so it shards correctly (shard())
// and never breaks the "/raw/{slug}.ext" embedding. An unknown/blank strategy
// falls back to the plain random slug so callers always get a usable value.
func GenerateSlug(strategy string, n int) (string, error) {
	switch strategy {
	case SlugStrategyGfycat:
		return gfycatSlug()
	case SlugStrategyDate:
		suffix, err := RandomSlug(n)
		if err != nil {
			return "", err
		}
		return time.Now().UTC().Format("2006-01-02") + "-" + suffix, nil
	default:
		return RandomSlug(n)
	}
}

// gfycatSuffixLen is how many random characters end a gfycat slug. The three
// words alone are ~23 bits (about 8M combinations), which anyone can walk in
// hours over the unthrottled public routes; the suffix adds 20 bits, more than
// a default-length random slug has in total.
const gfycatSuffixLen = 4

// gfycatSuffixAlphabet is slugAlphabet without its capitals, keeping the slug
// in the lowercase word style.
const gfycatSuffixAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// gfycatSlug joins two random adjectives, one random animal and a short random
// suffix with '-'. The words are curated lowercase-ASCII (see wordlist.go), so
// the result is always [a-z2-9-] and well over the 4-byte minimum.
func gfycatSlug() (string, error) {
	a1, err := randIndex(len(slugAdjectives))
	if err != nil {
		return "", err
	}
	a2, err := randIndex(len(slugAdjectives))
	if err != nil {
		return "", err
	}
	an, err := randIndex(len(slugAnimals))
	if err != nil {
		return "", err
	}
	suffix := make([]byte, gfycatSuffixLen)
	for i := range suffix {
		c, err := randIndex(len(gfycatSuffixAlphabet))
		if err != nil {
			return "", err
		}
		suffix[i] = gfycatSuffixAlphabet[c]
	}
	return slugAdjectives[a1] + "-" + slugAdjectives[a2] + "-" + slugAnimals[an] + "-" + string(suffix), nil
}

// randIndex returns a uniformly random index in [0, n) using crypto/rand with
// rejection sampling (a plain modulo would bias the low indices). This mirrors
// RandomSlug's approach; it never uses math/rand.
func randIndex(n int) (int, error) {
	if n <= 0 {
		return 0, errors.New("upload: randIndex requires n > 0")
	}
	const span = uint64(1) << 32
	// Largest multiple of n within [0, 2^32); values at or above it fall in the
	// short partial bucket that would bias the distribution, so reject them.
	limit := span - (span % uint64(n))
	var buf [4]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, err
		}
		v := uint64(binary.BigEndian.Uint32(buf[:]))
		if v >= limit {
			continue // would bias the distribution; draw again
		}
		// #nosec G115 -- v%uint64(n) is in [0, n) and n is a positive int.
		return int(v % uint64(n)), nil
	}
}
