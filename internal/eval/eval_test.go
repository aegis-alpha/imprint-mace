package eval

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// --- normalizeName ---

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Alice", "alice"},
		{"  ProtonDrive  ", "protondrive"},
		{"The Project", "project"},
		{"A Config", "config"},
		{"An Agent", "agent"},
		{"node-1", "node-1"},
	}
	for _, tt := range tests {
		got := normalizeName(tt.input)
		if got != tt.want {
			t.Errorf("normalizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Jaccard similarity ---

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		a, b string
		min  float64
	}{
		{"Alice prefers Telegram", "Alice prefers Telegram", 0.99},
		{"Alice prefers Telegram", "Alice likes Telegram updates", 0.3},
		{"completely different text", "nothing in common here", 0.0},
		{"", "", 0.99},
	}
	for _, tt := range tests {
		got := jaccardSimilarity(tt.a, tt.b)
		if got < tt.min {
			t.Errorf("jaccardSimilarity(%q, %q) = %.3f, want >= %.3f", tt.a, tt.b, got, tt.min)
		}
	}
}

// --- CosineSimilarity ---

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	got := CosineSimilarity(a, b)
	if math.Abs(got-1.0) > 0.001 {
		t.Errorf("CosineSimilarity(identical) = %f, want ~1.0", got)
	}

	c := []float32{0, 1, 0}
	got = CosineSimilarity(a, c)
	if math.Abs(got) > 0.001 {
		t.Errorf("CosineSimilarity(orthogonal) = %f, want ~0.0", got)
	}

	got = CosineSimilarity(nil, nil)
	if got != 0 {
		t.Errorf("CosineSimilarity(nil, nil) = %f, want 0", got)
	}

	got = CosineSimilarity([]float32{1}, []float32{1, 2})
	if got != 0 {
		t.Errorf("CosineSimilarity(different lengths) = %f, want 0", got)
	}
}

// --- FactMatchScore ---

func TestFactMatchScore(t *testing.T) {
	gold := GoldenFact{
		FactType: "decision",
		Subject:  "Acme",
		Content:  "Acme will be written in Go for single-binary deployment.",
	}

	exact := GoldenFact{
		FactType: "decision",
		Subject:  "Acme",
		Content:  "Acme will be written in Go for single-binary deployment.",
	}
	score := FactMatchScore(exact, gold)
	if score < 0.9 {
		t.Errorf("exact match score = %.3f, want >= 0.9", score)
	}

	wrongType := GoldenFact{
		FactType: "preference",
		Subject:  "Acme",
		Content:  "Acme will be written in Go for single-binary deployment.",
	}
	score = FactMatchScore(wrongType, gold)
	if score != 0 {
		t.Errorf("wrong type score = %.3f, want 0", score)
	}

	wrongSubject := GoldenFact{
		FactType: "decision",
		Subject:  "Completely Different Project",
		Content:  "Acme will be written in Go for single-binary deployment.",
	}
	score = FactMatchScore(wrongSubject, gold)
	if score > 0.5 {
		t.Errorf("wrong subject score = %.3f, want < 0.5", score)
	}
}

// --- EntityMatch ---

func TestEntityMatch(t *testing.T) {
	gold := GoldenEntity{Name: "Alice", EntityType: "person", Aliases: []string{"alice_v"}}

	if !EntityMatch(GoldenEntity{Name: "Alice"}, gold) {
		t.Error("exact name should match")
	}
	if !EntityMatch(GoldenEntity{Name: "alice"}, gold) {
		t.Error("case-insensitive should match")
	}
	if !EntityMatch(GoldenEntity{Name: "alice_v"}, gold) {
		t.Error("alias should match")
	}
	if EntityMatch(GoldenEntity{Name: "Bob"}, gold) {
		t.Error("different name should not match")
	}

	sysWithAlias := GoldenEntity{Name: "AV", Aliases: []string{"Alice"}}
	if !EntityMatch(sysWithAlias, gold) {
		t.Error("system alias matching gold name should match")
	}
}

func TestEntityTypeMatch(t *testing.T) {
	a := GoldenEntity{EntityType: "person"}
	b := GoldenEntity{EntityType: "Person"}
	if !EntityTypeMatch(a, b) {
		t.Error("case-insensitive type should match")
	}
	c := GoldenEntity{EntityType: "project"}
	if EntityTypeMatch(a, c) {
		t.Error("different types should not match")
	}
}

// --- RelationshipMatch ---

func TestRelationshipMatch(t *testing.T) {
	sysEnts := []GoldenEntity{
		{Name: "Alice", EntityType: "person"},
		{Name: "Acme", EntityType: "project"},
	}
	goldEnts := []GoldenEntity{
		{Name: "Alice", EntityType: "person"},
		{Name: "Acme", EntityType: "project", Aliases: []string{"acme-project"}},
	}

	sys := GoldenRelationship{FromEntity: "Alice", ToEntity: "Acme", RelationType: "works_on"}
	gold := GoldenRelationship{FromEntity: "Alice", ToEntity: "Acme", RelationType: "works_on"}
	if !RelationshipMatch(sys, gold, sysEnts, goldEnts) {
		t.Error("exact match should work")
	}

	wrongType := GoldenRelationship{FromEntity: "Alice", ToEntity: "Acme", RelationType: "owns"}
	if RelationshipMatch(wrongType, gold, sysEnts, goldEnts) {
		t.Error("wrong relation type should not match")
	}

	wrongEntity := GoldenRelationship{FromEntity: "Bob", ToEntity: "Acme", RelationType: "works_on"}
	if RelationshipMatch(wrongEntity, gold, sysEnts, goldEnts) {
		t.Error("wrong entity should not match")
	}
}

// --- ScoreFacts ---

func TestScoreFactsPerfect(t *testing.T) {
	facts := []GoldenFact{
		{FactType: "decision", Subject: "Acme", Content: "Acme uses Go"},
		{FactType: "preference", Subject: "Alice", Content: "Alice prefers Telegram"},
	}
	score := ScoreFacts(facts, facts, 0.5)
	if score.F1 < 0.99 {
		t.Errorf("perfect match F1 = %.3f, want ~1.0", score.F1)
	}
}

func TestScoreFactsEmpty(t *testing.T) {
	score := ScoreFacts(nil, nil, 0.5)
	if score.F1 < 0.99 {
		t.Errorf("both empty F1 = %.3f, want 1.0", score.F1)
	}

	score = ScoreFacts(nil, []GoldenFact{{FactType: "decision", Subject: "X", Content: "Y"}}, 0.5)
	if score.Recall != 0 {
		t.Errorf("system empty recall = %.3f, want 0", score.Recall)
	}

	score = ScoreFacts([]GoldenFact{{FactType: "decision", Subject: "X", Content: "Y"}}, nil, 0.5)
	if score.Precision != 0 {
		t.Errorf("gold empty precision = %.3f, want 0", score.Precision)
	}
}

func TestScoreFactsPartial(t *testing.T) {
	system := []GoldenFact{
		{FactType: "decision", Subject: "Acme", Content: "Acme uses Go"},
		{FactType: "decision", Subject: "Acme", Content: "Acme uses TOML config"},
		{FactType: "event", Subject: "Mars", Content: "Mars crashed due to OOM"},
	}
	gold := []GoldenFact{
		{FactType: "decision", Subject: "Acme", Content: "Acme uses Go"},
		{FactType: "decision", Subject: "Acme", Content: "Acme uses TOML for configuration"},
	}
	score := ScoreFacts(system, gold, 0.5)
	if score.Recall < 0.5 {
		t.Errorf("partial recall = %.3f, want >= 0.5", score.Recall)
	}
	if score.Precision > 0.9 {
		t.Errorf("partial precision = %.3f, want < 0.9 (spurious fact)", score.Precision)
	}
}

// --- ScoreEntities ---

func TestScoreEntitiesPerfect(t *testing.T) {
	ents := []GoldenEntity{
		{Name: "Alice", EntityType: "person"},
		{Name: "Acme", EntityType: "project"},
	}
	score := ScoreEntities(ents, ents)
	if score.F1 < 0.99 {
		t.Errorf("perfect entity F1 = %.3f, want ~1.0", score.F1)
	}
}

// --- ScoreNoise ---

func TestScoreNoise(t *testing.T) {
	result := ScoreNoise([]int{0, 0, 0, 2, 0})
	if math.Abs(result.NRR-0.8) > 0.01 {
		t.Errorf("NRR = %.3f, want 0.8", result.NRR)
	}
	if result.SpuriousFacts != 2 {
		t.Errorf("SpuriousFacts = %d, want 2", result.SpuriousFacts)
	}

	result = ScoreNoise(nil)
	if result.NRR != 1.0 {
		t.Errorf("empty NRR = %.3f, want 1.0", result.NRR)
	}
}

// --- ScoreCalibration ---

func TestScoreCalibrationPerfect(t *testing.T) {
	samples := []CalibrationSample{
		{Confidence: 0.9, Correct: true},
		{Confidence: 0.9, Correct: true},
		{Confidence: 0.1, Correct: false},
		{Confidence: 0.1, Correct: false},
	}
	result := ScoreCalibration(samples)
	if result.ECE > 0.15 {
		t.Errorf("well-calibrated ECE = %.4f, want < 0.15", result.ECE)
	}
}

func TestScoreCalibrationEmpty(t *testing.T) {
	result := ScoreCalibration(nil)
	if result.ECE != 0 {
		t.Errorf("empty ECE = %.4f, want 0", result.ECE)
	}
}

// --- CompositeScore ---

func TestCompositeScore(t *testing.T) {
	score := CompositeScore(1.0, 1.0, 1.0, 0.0, 1.0)
	if math.Abs(score-1.0) > 0.001 {
		t.Errorf("perfect composite = %.4f, want 1.0", score)
	}

	score = CompositeScore(0.0, 0.0, 0.0, 1.0, 0.0)
	if math.Abs(score) > 0.001 {
		t.Errorf("worst composite = %.4f, want 0.0", score)
	}
}

// --- LoadGoldenDir ---

func TestLoadGoldenDir(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "001-test.txt"), []byte("Alice said hello"), 0644)
	os.WriteFile(filepath.Join(dir, "001-test.json"), []byte(`{
		"facts": [{"fact_type": "event", "subject": "Alice", "content": "Alice said hello", "confidence": 0.9}],
		"entities": [{"name": "Alice", "entity_type": "person", "aliases": []}],
		"relationships": []
	}`), 0644)

	os.WriteFile(filepath.Join(dir, "002-noise.txt"), []byte("ok sounds good"), 0644)
	os.WriteFile(filepath.Join(dir, "002-noise.json"), []byte(`{"facts": [], "entities": [], "relationships": []}`), 0644)

	examples, err := LoadGoldenDir(dir)
	if err != nil {
		t.Fatalf("LoadGoldenDir: %v", err)
	}
	if len(examples) != 2 {
		t.Fatalf("got %d examples, want 2", len(examples))
	}
	if examples[0].Name != "001-test" {
		t.Errorf("first example name = %q, want 001-test", examples[0].Name)
	}
	if examples[0].Expected.IsNoise() {
		t.Error("first example should not be noise")
	}
	if !examples[1].Expected.IsNoise() {
		t.Error("second example should be noise")
	}
}

func TestLoadGoldenDirEmpty(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadGoldenDir(dir)
	if err == nil {
		t.Error("expected error for empty dir")
	}
}
