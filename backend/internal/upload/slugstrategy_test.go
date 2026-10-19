package upload

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

// slugFromParam mirrors the (unexported) api.slugFromParam helper: it strips an
// optional file extension from a raw-URL slug. Copied here so we can assert that
// a generated slug round-trips through the "/raw/{slug}.ext" path unchanged.
func slugFromParam(param string) string {
	if i := strings.LastIndexByte(param, '.'); i > 0 {
		return param[:i]
	}
	return param
}

var (
	// The URL-safe slug invariant: only ASCII letters/digits/hyphen, no dot, and
	// long enough that shard() stays valid. RandomSlug's alphabet is mixed-case,
	// so the random/date variants legitimately contain A-Z.
	reSlugCharset = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	reGfycat      = regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+-[a-km-np-z2-9]{4}$`)
	reDate        = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-[A-Za-z0-9]+$`)
)

// assertSlugInvariants checks the guarantees every generated slug must uphold.
func assertSlugInvariants(t *testing.T, label, slug string) {
	t.Helper()
	if !reSlugCharset.MatchString(slug) {
		t.Errorf("%s slug %q is not [A-Za-z0-9-]", label, slug)
	}
	if strings.Contains(slug, ".") {
		t.Errorf("%s slug %q contains a dot", label, slug)
	}
	if len(slug) < 4 {
		t.Errorf("%s slug %q is shorter than 4 bytes", label, slug)
	}
	// A generated slug must survive the raw-URL extension stripping untouched.
	if got := slugFromParam(slug + ".png"); got != slug {
		t.Errorf("%s slug %q did not round-trip: slugFromParam(%q) = %q", label, slug, slug+".png", got)
	}
	// It must also be an acceptable custom slug so it never trips other guards.
	if !ValidCustomSlug(slug) {
		t.Errorf("%s slug %q rejected by ValidCustomSlug", label, slug)
	}
}

func TestGenerateSlugStrategies(t *testing.T) {
	for i := 0; i < 300; i++ {
		g, err := GenerateSlug(SlugStrategyGfycat, 6)
		if err != nil {
			t.Fatalf("gfycat: %v", err)
		}
		assertSlugInvariants(t, "gfycat", g)
		if !reGfycat.MatchString(g) {
			t.Errorf("gfycat slug %q does not match adjective-adjective-animal-suffix shape", g)
		}

		d, err := GenerateSlug(SlugStrategyDate, 6)
		if err != nil {
			t.Fatalf("date: %v", err)
		}
		assertSlugInvariants(t, "date", d)
		if !reDate.MatchString(d) {
			t.Errorf("date slug %q does not match YYYY-MM-DD-<random> shape", d)
		}

		r, err := GenerateSlug(SlugStrategyRandom, 6)
		if err != nil {
			t.Fatalf("random: %v", err)
		}
		assertSlugInvariants(t, "random", r)
	}
}

func TestGenerateSlugUnknownFallsBackToRandom(t *testing.T) {
	// Blank and unrecognized strategies (including the dropped "emoji"/"zalgo" and
	// a wrong-case value) must degrade to a plain random slug.
	for _, strategy := range []string{"", "bogus", "GFYCAT", "emoji", "zalgo", "  "} {
		s, err := GenerateSlug(strategy, 6)
		if err != nil {
			t.Fatalf("GenerateSlug(%q): %v", strategy, err)
		}
		assertSlugInvariants(t, "fallback", s)
		if strings.Contains(s, "-") {
			t.Errorf("fallback slug %q unexpectedly contains '-' (not a plain random slug)", s)
		}
		if len(s) != 6 {
			t.Errorf("fallback slug %q length = %d, want 6", s, len(s))
		}
	}
}

func TestValidSlugStrategy(t *testing.T) {
	for _, s := range []string{"random", "gfycat", "date"} {
		if !ValidSlugStrategy(s) {
			t.Errorf("ValidSlugStrategy(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "Random", "GFYCAT", "gfy", "dates", "emoji", "zalgo", " random "} {
		if ValidSlugStrategy(s) {
			t.Errorf("ValidSlugStrategy(%q) = true, want false", s)
		}
	}
}

func TestGfycatLowDuplicateRate(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	dupes := 0
	for i := 0; i < n; i++ {
		s, err := GenerateSlug(SlugStrategyGfycat, 6)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := seen[s]; ok {
			dupes++
		}
		seen[s] = struct{}{}
	}
	// Keyspace is ~len(adj)^2 * len(animals) * 32^4 ≈ 2^43, so a single
	// duplicate over 1000 draws already means the suffix is not random.
	if dupes > 0 {
		t.Errorf("gfycat duplicate rate too high: %d duplicates over %d draws", dupes, n)
	}
}

// The words alone are guessable in hours; with the suffix a gfycat slug has to
// carry at least as much entropy as a default random slug (56^6 ≈ 2^34.8).
func TestGfycatEntropy(t *testing.T) {
	bits := math.Log2(float64(len(slugAdjectives))*float64(len(slugAdjectives))*float64(len(slugAnimals))) +
		float64(gfycatSuffixLen)*math.Log2(float64(len(gfycatSuffixAlphabet)))
	if bits < 6*math.Log2(float64(len(slugAlphabet))) {
		t.Fatalf("gfycat slugs carry %.1f bits, less than a 6-character random slug", bits)
	}
	if strings.ContainsAny(gfycatSuffixAlphabet, "ABCDEFGHIJKLMNOPQRSTUVWXYZ-") {
		t.Fatal("gfycat suffix alphabet breaks the lowercase word style")
	}
}
