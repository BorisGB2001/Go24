package crm

import (
	"math"
	"testing"
)

// TestScoreBuckets: the counts, the amounts by quartile, no amount — "zero".
func TestScoreBuckets(t *testing.T) {
	for n, want := range map[int]string{0: "0", 1: "1-2", 2: "1-2", 3: "3-5", 5: "3-5", 6: "6+", 40: "6+"} {
		if got := countBucket(n); got != want {
			t.Errorf("countBucket(%d) = %s, want %s", n, got, want)
		}
	}
	b := quartiles([]float64{0, 100, 200, 300, 400, 500, 600, 700, 800})
	if len(b) != 3 || b[0] != 200 || b[1] != 400 || b[2] != 600 {
		t.Fatalf("quartiles: %v", b)
	}
	for a, want := range map[float64]string{0: "zero", 50: "q1", 200: "q1", 250: "q2", 600: "q3", 10000: "q4"} {
		if got := amountBucket(a, b); got != want {
			t.Errorf("amountBucket(%v) = %s, want %s", a, got, want)
		}
	}
	if b := quartiles(nil); len(b) != 3 {
		t.Fatalf("no amounts: %v", b)
	}
}

// TestScoreModel: what the won deals are like raises the probability, what the lost are like lowers it; leave-one-out
// accuracy does not count a deal by itself.
func TestScoreModel(t *testing.T) {
	deal := func(origin string) map[string]string {
		v := map[string]string{}
		for _, f := range ScoreFeatures {
			v[f] = "x"
		}
		v["origin"] = origin
		return v
	}
	var values []map[string]string
	var won []bool
	for i := 0; i < 10; i++ {
		values, won = append(values, deal("site")), append(won, true)
		values, won = append(values, deal("cold")), append(won, false)
	}
	m := train(values, won)
	if m.Won != 10 || m.Lost != 10 || m.Counts["origin"]["site"] != [2]int{10, 0} {
		t.Fatalf("model: %+v", m)
	}
	site, parts := m.probability(deal("site"), nil)
	cold, _ := m.probability(deal("cold"), nil)
	other, _ := m.probability(deal("ads"), nil)
	if site < 0.9 || cold > 0.1 || math.Abs(other-0.5) > 0.01 {
		t.Fatalf("site %v, cold %v, unseen %v", site, cold, other)
	}
	if parts["origin"] <= 0 || math.Abs(parts["client"]) > 1e-9 {
		t.Fatalf("parts: %v", parts)
	}
	if acc := m.accuracy(values, won); acc != 1 {
		t.Fatalf("accuracy: %v", acc)
	}
	// one deal of its own kind: without itself the model has not seen it
	values, won = append(values, deal("fair")), append(won, true)
	m = train(values, won)
	if p, _ := m.probability(deal("fair"), &won[len(won)-1]); p > 0.6 {
		t.Fatalf("left out: %v", p)
	}
	d := scoreDeal{values: deal("site"), dicts: map[string]Dict{"origin": {Name: "Сайт"}}, responsible: "Анна"}
	f := d.factors(map[string]float64{"origin": 2, "responsible": 0.5, "amount": -1, "client": 0.01})
	if len(f) != 3 || f[0].Feature != "origin" || f[0].Dict.Name != "Сайт" || f[1].Name != "Анна" || f[2].Up {
		t.Fatalf("factors: %+v", f)
	}
}
