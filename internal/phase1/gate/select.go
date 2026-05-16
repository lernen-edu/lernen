package gate

import (
	"hash/fnv"
	"math/rand"
	"sort"

	"github.com/lernen-edu/lernen/internal/languages"
)

// SelectSet deterministically picks 1 build + 3 comprehension + 1
// debug-per-tier from (curriculumID, attemptNumber). Same inputs =>
// same set (so a resumed attempt re-selects identically); a new
// attemptNumber rotates the bank (spec §2.3).
func SelectSet(gf languages.GateFixtures, curriculumID string, attemptNumber int) FixtureSet {
	h := fnv.New64a()
	_, _ = h.Write([]byte(curriculumID))
	_, _ = h.Write([]byte{byte(attemptNumber), byte(attemptNumber >> 8)})
	// Determinism is a persisted contract (resumed attempts must re-select
	// identically; selections are recorded in the durable log). It depends
	// on math/rand v1's stable seeded algorithm — do NOT migrate to
	// math/rand/v2 (which disclaims output stability) without a schema
	// bump + regenerating persisted sidecars.
	rng := rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // not security-sensitive

	fs := FixtureSet{}
	bIDs := ids(len(gf.Build), func(i int) string { return gf.Build[i].ID })
	fs.Build = pick(rng, bIDs, 1)[0]

	cIDs := ids(len(gf.Comprehension), func(i int) string { return gf.Comprehension[i].ID })
	fs.Comprehension = sortedPick(rng, cIDs, 3)

	byTier := map[int][]string{}
	for _, d := range gf.Debug {
		byTier[d.Tier] = append(byTier[d.Tier], d.ID)
	}
	for tier := 1; tier <= 3; tier++ {
		t := append([]string(nil), byTier[tier]...)
		sort.Strings(t)
		fs.Debug = append(fs.Debug, pick(rng, t, 1)[0])
	}
	return fs
}

func ids(n int, get func(int) string) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = get(i)
	}
	sort.Strings(out)
	return out
}

func pick(rng *rand.Rand, pool []string, k int) []string {
	cp := append([]string(nil), pool...)
	rng.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
	if k > len(cp) {
		k = len(cp)
	}
	return cp[:k]
}

func sortedPick(rng *rand.Rand, pool []string, k int) []string {
	p := pick(rng, pool, k)
	sort.Strings(p)
	return p
}
