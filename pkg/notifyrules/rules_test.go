package notifyrules

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type vectorFile struct {
	Description string   `json:"description"`
	Version     int      `json:"version"`
	Vectors     []vector `json:"vectors"`
}

type vector struct {
	Name  string   `json:"name"`
	Input Input    `json:"input"`
	Want  Decision `json:"want"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notify-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f vectorFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("testdata/notify-rules.json: %v", err)
	}
	if f.Version != 1 {
		t.Fatalf("vector file version = %d, want 1", f.Version)
	}
	return f.Vectors
}

func TestRulesVectors(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors) < 40 {
		t.Fatalf("only %d vectors, want at least 40", len(vectors))
	}
	seen := map[string]bool{}
	for _, v := range vectors {
		if seen[v.Name] {
			t.Errorf("duplicate vector name %q", v.Name)
		}
		seen[v.Name] = true
		t.Run(v.Name, func(t *testing.T) {
			got := Decide(v.Input)
			if got != v.Want {
				t.Errorf("Decide = %+v, want %+v", got, v.Want)
			}
		})
	}
}

// TestRulesVectorsCoverEveryReason fails when a rule has no vector, so a new
// rule cannot ship untested.
func TestRulesVectorsCoverEveryReason(t *testing.T) {
	reasons := []string{
		ReasonSystemMessage, ReasonUnknownKind, ReasonOwnMessage, ReasonMuted, ReasonAlertTransition,
		ReasonAlertSeverity, ReasonBelowMinSeverity, ReasonDirect, ReasonLevelAll, ReasonMentioned,
		ReasonChannelMention, ReasonHereMention, ReasonCriticalAlert, ReasonNotMentioned, ReasonLevelNone,
		ReasonUnknownLevel, ReasonUnknownMinSeverity,
	}
	covered := map[string]bool{}
	for _, v := range loadVectors(t) {
		covered[v.Want.Reason] = true
	}
	for _, r := range reasons {
		if !covered[r] {
			t.Errorf("no vector decides with reason %q", r)
		}
	}
}

// TestRulesGuestIsIrrelevant: the verdict depends on membership settings, never
// on the recipient's role, so Input has no role field.
func TestRulesGuestIsIrrelevant(t *testing.T) {
	typ := reflect.TypeOf(Input{})
	for i := range typ.NumField() {
		if name := strings.ToLower(typ.Field(i).Name); strings.Contains(name, "role") || strings.Contains(name, "guest") {
			t.Errorf("Input.%s makes the verdict depend on the recipient's role", typ.Field(i).Name)
		}
	}
}

func TestRulesDecisionIsDeterministic(t *testing.T) {
	for _, v := range loadVectors(t) {
		first := Decide(v.Input)
		for range 3 {
			if again := Decide(v.Input); again != first {
				t.Fatalf("%s: Decide is not deterministic: %+v then %+v", v.Name, first, again)
			}
		}
	}
}

// TestRulesStdlibOnly keeps the package free of non-standard imports: the
// tray app and the SDK depend on it.
func TestRulesStdlibOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %q: pkg/notifyrules must use the standard library only", name, path)
			}
		}
	}
}
